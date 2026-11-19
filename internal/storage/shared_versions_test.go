package storage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func TestSharedPhysicalWriteVersions(t *testing.T) {
	for _, mode := range []string{"plain", "encrypted", "filename-off"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			pool := testutil.Database(t)
			fixture, cfg := testutil.ObjectStore(t)
			fixture.RejectConditionalWrites.Store(true)
			upstream, _ := url.Parse(fixture.Server.URL)
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			proxy.ModifyResponse = func(r *http.Response) error { r.Header.Del("X-Amz-Version-Id"); return nil }
			front := httptest.NewServer(proxy)
			defer front.Close()
			endpoint, _ := url.Parse(front.URL)
			cfg.PrivateEndpoints += "," + endpoint.Host
			repo := &store.Repository{Pool: pool}
			c := store.Connection{Owner: "33333333-3333-4333-8333-333333333333", Name: "Shared", Provider: "Other", Endpoint: front.URL, Region: "us-east-1", PathStyle: true, Shared: true, AccessKey: "test", SecretKey: "test"}
			if err := repo.SaveConnection(store.WithAdmin(ctx, true), &c); err != nil {
				t.Fatal(err)
			}
			handles := make([]storage.Store, 2)
			books := make([]store.Notebook, 2)
			keys := []string{testutil.Owner + "/notes/shared.md", testutil.Other + "/notes/shared.md"}
			for i, owner := range []string{testutil.Owner, testutil.Other} {
				n := store.Notebook{Owner: owner, ConnectionID: c.ID, Name: "Shared", Bucket: "test", Encrypted: mode != "plain", FilenameEncryption: "standard", FilenameEncoding: "base32", DirectoryEncryption: true, Suffix: ".bin"}
				if mode == "filename-off" {
					n.FilenameEncryption = "off"
				}
				if err := repo.SaveNotebook(ctx, &n, store.Secrets{Password: "shared crypt password"}); err != nil {
					t.Fatal(err)
				}
				books[i] = n
				registry := &storage.Registry{Repo: &store.Repository{Pool: pool}, Config: cfg}
				var err error
				handles[i], _, err = registry.Open(ctx, owner, n.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			first, err := handles[0].Put(ctx, keys[0], []byte("initial"), "*", "text/markdown")
			if err != nil {
				t.Fatal(err)
			}
			_, other, err := handles[1].Get(ctx, keys[1], "", 4096)
			if err != nil {
				t.Fatal(err)
			}
			// Identical-content updates must invalidate the other mapping's token too.
			if _, err = handles[0].Put(ctx, keys[0], []byte("initial"), first.Validator(), "text/markdown"); err != nil {
				t.Fatal(err)
			}
			if _, err = handles[1].Put(ctx, keys[1], []byte("stale"), other.Validator(), "text/markdown"); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("other owner's stale validator accepted", err)
			}
			for _, same := range []bool{false, true} {
				tokens := make([]string, 2)
				for i, h := range handles {
					_, m, err := h.Get(ctx, keys[i], "", 4096)
					if err != nil {
						t.Fatal(err)
					}
					tokens[i] = m.Validator()
				}
				start := make(chan struct{})
				results := make(chan error, 2)
				for i, h := range handles {
					go func(i int, h storage.Store) {
						<-start
						body := []byte{byte('a' + i)}
						if same {
							body = []byte("identical")
						}
						_, err := h.Put(ctx, keys[i], body, tokens[i], "text/markdown")
						results <- err
					}(i, h)
				}
				close(start)
				wins, conflicts := 0, 0
				for range 2 {
					err := <-results
					if err == nil {
						wins++
					} else if errors.Is(err, storage.ErrConflict) {
						conflicts++
					} else {
						t.Fatal(err)
					}
				}
				if wins != 1 || conflicts != 1 {
					t.Fatal("shared concurrent saves", wins, conflicts)
				}
			}
			_, current, err := handles[1].Get(ctx, keys[1], "", 4096)
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.RemoveNotebook(ctx, books[0].Owner, books[0].ID, books[0].Revision); err != nil {
				t.Fatal(err)
			}
			if _, _, err = handles[0].Get(ctx, keys[0], "", 4096); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("removed notebook authorized", err)
			}
			books[0].ID = ""
			if err = repo.SaveNotebook(ctx, &books[0], store.Secrets{Password: "shared crypt password"}); err != nil {
				t.Fatal(err)
			}
			registry := &storage.Registry{Repo: repo, Config: cfg}
			handles[0], _, err = registry.Open(ctx, books[0].Owner, books[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			_, reattached, err := handles[0].Get(ctx, keys[0], "", 4096)
			if err != nil || reattached.Validator() != current.Validator() {
				t.Fatal("mapping removal reset shared revision", reattached, err)
			}
			if err = handles[0].Delete(ctx, keys[0], current.Validator()); err != nil {
				t.Fatal(err)
			}
			recreated, err := handles[1].Put(ctx, keys[1], []byte("recreated"), "*", "text/markdown")
			if err != nil {
				t.Fatal(err)
			}
			if err = handles[0].Delete(ctx, keys[0], current.Validator()); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("old delete accepted after recreate", err)
			}
			if _, err = handles[0].Put(ctx, keys[0], nil, current.Validator(), "text/markdown"); !errors.Is(err, storage.ErrConflict) {
				t.Fatal("old update accepted after recreate", err)
			}
			for _, n := range books {
				if err = repo.RemoveNotebook(ctx, n.Owner, n.ID, n.Revision); err != nil {
					t.Fatal(err)
				}
			}
			// Saving a detached connection can relocate identical provider data.
			// This invalidates retained tokens even if metadata remains unchanged.
			c.Name = "Detached"
			if err = repo.SaveConnection(store.WithAdmin(ctx, true), &c); err != nil {
				t.Fatal(err)
			}
			books[0].ID = ""
			if err = repo.SaveNotebook(ctx, &books[0], store.Secrets{Password: "shared crypt password"}); err != nil {
				t.Fatal(err)
			}
			handle, _, err := registry.Open(ctx, books[0].Owner, books[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			_, detached, err := handle.Get(ctx, keys[0], "", 4096)
			if err != nil || detached.Validator() == recreated.Validator() {
				t.Fatal("detached save retained old token", err)
			}

		})
	}
}

func TestPhysicalVersionKeyBoundsAndAuthorization(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	f, cfg := testutil.ObjectStore(t)
	repo := &store.Repository{Pool: pool}
	registry := &storage.Registry{Repo: repo, Config: cfg}
	c := store.Connection{Owner: testutil.Owner, Name: "Bounds", Provider: "Other", Endpoint: f.Server.URL, Region: "us-east-1", PathStyle: true, AccessKey: "test", SecretKey: "test"}
	if err := repo.SaveConnection(ctx, &c); err != nil {
		t.Fatal(err)
	}
	key := testutil.Owner + "/notes/" + strings.Repeat("k", 759) + ".md"
	for _, kind := range []string{"limit", "prefix-overflow", "crypt-overflow"} {
		n := store.Notebook{Owner: testutil.Owner, ConnectionID: c.ID, Name: kind, Bucket: "test", Prefix: strings.Repeat("p", 255), Encrypted: kind == "crypt-overflow", FilenameEncryption: "standard", FilenameEncoding: "base32", DirectoryEncryption: true, Suffix: ".bin"}
		if kind == "prefix-overflow" {
			n.Prefix = strings.Repeat("q", 256)
		}
		if kind == "crypt-overflow" {
			n.Prefix = "crypt"
		}
		if err := repo.SaveNotebook(ctx, &n, store.Secrets{Password: "crypt secret"}); err != nil {
			t.Fatal(err)
		}
		handle, _, err := registry.Open(ctx, n.Owner, n.ID)
		if err != nil {
			t.Fatal(err)
		}
		before := f.Heads.Load()
		_, err = handle.Put(ctx, key, []byte("body"), "*", "text/markdown")
		if kind == "limit" {
			if err != nil {
				t.Fatal("1024-byte physical key rejected", err)
			}
		} else if !errors.Is(err, storage.ErrPath) || f.Heads.Load() != before {
			t.Fatal("overlong physical key reached S3", kind, err)
		}
		unauthorized := &storage.Managed{Registry: registry, Owner: testutil.Other, ID: n.ID}
		if _, err = unauthorized.Head(ctx, testutil.Other+"/notes/secret.md"); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("foreign notebook touched coordination", err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_write_versions WHERE scope=$1 AND owner=$2 AND octet_length(key)=1029`, c.ID, c.Owner).Scan(&count); err != nil || count != 1 {
		t.Fatal("physical key identity", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_write_versions`).Scan(&count); err != nil || count != 1 {
		t.Fatal("invalid key created coordination metadata", count, err)
	}
}
