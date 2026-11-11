// Package testutil provides disposable PostgreSQL schemas and an S3 fixture.
package testutil

import (
	"context"
	"database/sql"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/db"
	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/rclone/gofakes3"
	"github.com/rclone/gofakes3/s3mem"
)

const Owner = "11111111-1111-4111-8111-111111111111"
const Other = "22222222-2222-4222-8222-222222222222"

func Database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set disposable loopback TEST_DATABASE_URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		t.Fatal("loopback PostgreSQL required")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := "notes_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	pool, err := pgxpool.New(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		database.Close()
		admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		admin.Close()
	})
	if err = db.Migrate(context.Background(), database, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

type S3 struct {
	Store                       storage.Store
	Raw                         storage.Store
	Server                      *httptest.Server
	Gets, Heads, Lists          atomic.Int64
	FailList, FailHead, FailPut atomic.Bool
	RejectConditionalWrites     atomic.Bool
	mutex                       sync.Mutex
	Before                      func(*http.Request)
}

func ObjectStore(t *testing.T) (*S3, config.Config) {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	fixture := &S3{}
	h := gofakes3.New(backend).Server()
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mutex.Lock()
		defer fixture.mutex.Unlock()
		if (r.Method == "PUT" || r.Method == "DELETE") && fixture.RejectConditionalWrites.Load() && (r.Header.Get("If-Match") != "" || r.Header.Get("If-None-Match") != "") {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(501)
			io.WriteString(w, `<Error><Code>NotImplemented</Code><Message>Unsupported functionality</Message><Header>If-None-Match</Header></Error>`)
			return
		}
		if r.Method == "PUT" && fixture.FailPut.Load() {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(400)
			io.WriteString(w, "<Error><Code>InvalidArgument</Code><Message>private note path and credential</Message></Error>")
			return
		}
		if fixture.Before != nil {
			fixture.Before(r)
		}
		if r.Method == "GET" && r.URL.Query().Get("list-type") == "2" {
			fixture.Lists.Add(1)
			if fixture.FailList.Load() {
				http.Error(w, "transient", 503)
				return
			}
		}
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		if len(parts) == 2 {
			if r.Method == "GET" {
				fixture.Gets.Add(1)
			}
			if r.Method == "HEAD" {
				fixture.Heads.Add(1)
				if fixture.FailHead.Load() {
					http.Error(w, "transient", 503)
					return
				}
			}
			obj, err := backend.HeadObject(r.Context(), parts[0], parts[1])
			etag := ""
			if err == nil {
				etag = `"` + hex.EncodeToString(obj.Hash) + `"`
			}
			if (r.Header.Get("If-None-Match") == "*" && err == nil) || (r.Header.Get("If-Match") != "" && (err != nil || r.Header.Get("If-Match") != etag)) {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(412)
				io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(fixture.Server.Close)
	u, _ := url.Parse(fixture.Server.URL)
	cfg := config.Config{Origin: "http://notes.localhost:8080", PrivateEndpoints: u.Host, AllowHTTP: true, ScanInterval: 3600000000000, Workers: 4, MaxNoteBytes: 2 << 20, MaxAttachmentBytes: 32 << 20}
	var err error
	fixture.Store, err = storage.New(storage.S3Config{Endpoint: fixture.Server.URL, Region: "us-east-1", Bucket: "test", AccessKey: "test", SecretKey: "test", PrivateEndpoints: u.Host, AllowHTTP: true, PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	fixture.Raw = fixture.Store
	return fixture, cfg
}

// Attach creates durable settings; callers may replace fixture.Store with a
// direct mapping to simulate out-of-band changes that bypass service policy.
func Attach(t *testing.T, pool *pgxpool.Pool, fixture *S3, cfg config.Config, owner, prefix string, encrypted bool, password string) (*storage.Registry, store.Notebook) {
	t.Helper()
	registry := &storage.Registry{Repo: &store.Repository{Pool: pool}, Config: cfg}
	c := store.Connection{Owner: owner, Name: "Test " + prefix, Provider: "Other", Endpoint: fixture.Server.URL, Region: "us-east-1", PathStyle: true, ConditionalWrites: true, AccessKey: "test", SecretKey: "test"}
	if err := registry.Repo.SaveConnection(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	n := store.Notebook{Owner: owner, ConnectionID: c.ID, Name: "Notebook " + prefix, Bucket: "test", Prefix: prefix, Encrypted: encrypted, FilenameEncryption: "standard", FilenameEncoding: "base32", DirectoryEncryption: true, Suffix: ".bin"}
	if err := registry.Repo.SaveNotebook(context.Background(), &n, store.Secrets{Password: password}); err != nil {
		t.Fatal(err)
	}
	return registry, n
}
