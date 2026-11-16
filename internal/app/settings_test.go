package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/razzie-cloud/notes/internal/app"
	"github.com/razzie-cloud/notes/internal/index"
	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func TestUserStorageSetupAndEncryptedRecovery(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	fixture, cfg := testutil.ObjectStore(t)
	registry := &storage.Registry{Repo: &store.Repository{Pool: pool}, Config: cfg}
	engine, err := index.New(pool, registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(cfg, engine)
	if err != nil {
		t.Fatal(err)
	}
	handler := a.Handler()
	request := func(method, path string, body url.Values, user string) *httptest.ResponseRecorder {
		var data string
		if body != nil {
			data = body.Encode()
		}
		r := httptest.NewRequest(method, path, strings.NewReader(data))
		r.Header.Set("X-Auth-Request-User", user)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", cfg.Origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/", nil, testutil.Owner)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Add a connection") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/storage", nil, testutil.Owner)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `name="conditional_writes" id="conditional-writes" aria-describedby="conditional-writes-help" checked`) {
		t.Fatal("new connections must default to conditional writes", w.Code)
	}
	connection := url.Values{"name": {"My S3"}, "provider": {"Other"}, "endpoint": {fixture.Server.URL}, "region": {"us-east-1"}, "path_style": {"on"}, "conditional_writes": {"on"}, "access_key": {"test"}, "secret_key": {"test"}, "session_token": {"session-test-secret"}}
	w = request("POST", "/connections", connection, testutil.Owner)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	connections, err := registry.Repo.Connections(ctx, testutil.Owner)
	if err != nil || len(connections) != 1 {
		t.Fatal(connections, err)
	}
	conn := connections[0]
	if !conn.ConditionalWrites {
		t.Fatal("conditional write setting not saved")
	}
	if conn.AccessKey != "" || conn.SecretKey != "" || conn.SessionToken != "" {
		t.Fatal("connection query exposed credentials")
	}
	w = request("GET", "/storage?connection="+conn.ID, nil, testutil.Owner)
	if w.Code != 200 || strings.Contains(w.Body.String(), "session-test-secret") || strings.Contains(w.Body.String(), `value="test"`) {
		t.Fatal("secret in form", w.Code)
	}
	if w = request("GET", "/storage?connection="+conn.ID, nil, testutil.Other); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	notebook := url.Values{"name": {"Encrypted notes"}, "connection_id": {conn.ID}, "bucket": {"test"}, "prefix": {"encrypted-ui"}, "encrypted": {"on"}, "crypt_password": {"my crypt password"}, "crypt_password2": {"salt"}, "filename_encryption": {"standard"}, "filename_encoding": {"base32"}, "directory_encryption": {"on"}, "suffix": {".bin"}, "new_crypt_prefix": {"on"}}
	w = request("POST", "/notebooks", notebook, testutil.Owner)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	books, err := registry.Repo.Notebooks(ctx, testutil.Owner)
	if err != nil || len(books) != 1 {
		t.Fatal(books, err)
	}
	book := books[0]
	if w = request("GET", "/storage?notebook="+book.ID, nil, testutil.Other); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w = request("GET", "/recovery/"+book.SecretID, nil, testutil.Other); w.Code != 404 {
		t.Fatal(w.Code)
	}
	w = request("GET", "/recovery/"+book.SecretID, nil, testutil.Owner)
	var recovery store.Recovery
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &recovery) != nil || recovery.Password != "my crypt password" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal(w.Code, w.Body.String())
	}
	bound, err := engine.Bind(ctx, testutil.Owner, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bound.Storage.Put(ctx, testutil.Owner+"/notes/import.md", []byte("# Secret note"), "*", "text/markdown"); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/edit?notebook="+book.ID+"&key=import.md", nil, testutil.Owner)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Secret note") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("GET", "/v1/notes/import.md?notebook="+book.ID, nil, testutil.Other); w.Code != 404 {
		t.Fatal(w.Code)
	}
	// An occupied prefix cannot be initialized with a different crypt password.
	notebook.Set("name", "Wrong crypt")
	notebook.Set("crypt_password", "wrong")
	if w = request("POST", "/notebooks", notebook, testutil.Owner); w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Detach, retain objects and recovery, then prove credentials before reattach.
	w = request("POST", "/notebooks/"+book.ID+"/delete", url.Values{"revision": {fmt.Sprint(book.Revision)}, "confirm": {"on"}}, testutil.Owner)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("GET", "/recovery/"+book.SecretID, nil, testutil.Owner); w.Code != 200 {
		t.Fatal("recovery lost")
	}
	notebook.Del("new_crypt_prefix")
	notebook.Set("verify_path", "notes/import.md")
	if w = request("POST", "/notebooks", notebook, testutil.Owner); w.Code != 422 {
		t.Fatal("wrong crypt accepted", w.Code, w.Body.String())
	}
	notebook.Set("crypt_password", "my crypt password")
	notebook.Set("name", "Reattached")
	w = request("POST", "/notebooks", notebook, testutil.Owner)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	books, err = registry.Repo.Notebooks(ctx, testutil.Owner)
	if err != nil || len(books) != 1 {
		t.Fatal(err)
	}
	reattached := books[0]
	if w = request("GET", "/v1/notes/import.md?notebook="+reattached.ID, nil, testutil.Owner); w.Code != 200 || w.Body.String() != "# Secret note" {
		t.Fatal(w.Code, w.Body.String())
	}
	// Rotate policy while preserving credential fields that were left blank.
	connection.Set("revision", fmt.Sprint(conn.Revision))
	connection.Set("read_only", "on")
	connection.Set("access_key", "")
	connection.Set("secret_key", "")
	connection.Set("session_token", "")
	connection.Del("conditional_writes")
	w = request("POST", "/connections/"+conn.ID, connection, testutil.Owner)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	credentials, err := registry.Repo.Credentials(ctx, testutil.Owner, conn.ID)
	if err != nil || credentials.AccessKey != "test" || credentials.SecretKey != "test" || credentials.SessionToken != "session-test-secret" || credentials.ConditionalWrites {
		t.Fatal("credentials not retained", err)
	}
	w = request("GET", "/storage?connection="+conn.ID, nil, testutil.Owner)
	if w.Code != 200 || strings.Contains(w.Body.String(), `aria-describedby="conditional-writes-help" checked`) || !strings.Contains(w.Body.String(), "Conditional writes off") {
		t.Fatal("disabled conditional write setting not shown", w.Code)
	}
	r := httptest.NewRequest(http.MethodPut, "/v1/notes/forbidden.md?notebook="+reattached.ID, strings.NewReader("write"))
	r.Header.Set("X-Auth-Request-User", testutil.Owner)
	r.Header.Set("If-None-Match", "*")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	// The earlier bound handle also observes the new connection-level policy.
	current, err := engine.Bind(ctx, testutil.Owner, reattached.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = current.Storage.Put(ctx, testutil.Owner+"/notes/forbidden.md", []byte("write"), "*", "text/markdown"); err != store.ErrReadOnly {
		t.Fatal(err)
	}
	if w = request("POST", "/connections/"+conn.ID, connection, testutil.Owner); w.Code != 409 {
		t.Fatal("stale configuration revision accepted", w.Code)
	}
}
