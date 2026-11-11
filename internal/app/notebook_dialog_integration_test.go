package app

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/razzie-cloud/notes/internal/index"
	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func TestNotebookDialogWorkflow(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	fixture, cfg := testutil.ObjectStore(t)
	repo := &store.Repository{Pool: pool}
	registry := &storage.Registry{Repo: repo, Config: cfg}
	engine, err := index.New(pool, registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, engine)
	if err != nil {
		t.Fatal(err)
	}
	handler := a.Handler()
	request := func(method, path, user, target string, values url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
		req.Header.Set("X-Auth-Request-User", user)
		req.Header.Set("Origin", cfg.Origin)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if target != "" {
			req.Header.Set("HX-Request", "true")
			req.Header.Set("HX-Target", target)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	out := request("GET", "/notebooks", testutil.Owner, "", nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "Add connection") || strings.Contains(out.Body.String(), "data-notebook-form") {
		t.Fatal("no connection", out.Code, out.Body.String())
	}
	c := store.Connection{Owner: testutil.Owner, Name: "Storage", Provider: "Other", Endpoint: fixture.Server.URL, Region: "us-east-1", PathStyle: true, ConditionalWrites: true, ReadOnly: true, AccessKey: "stored-access-sentinel", SecretKey: "stored-secret-sentinel"}
	if err = repo.SaveConnection(ctx, &c); err != nil {
		t.Fatal(err)
	}
	out = request("GET", "/notebooks", testutil.Owner, "", nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "data-notebook-form") {
		t.Fatal("first notebook", out.Code, out.Body.String())
	}
	values := url.Values{"name": {"Encrypted notes"}, "connection_id": {c.ID}, "bucket": {"test"}, "prefix": {"encrypted-ui"}, "encrypted": {"on"}, "crypt_password": {"submitted-crypt-sentinel"}, "crypt_password2": {"submitted-salt-sentinel"}, "filename_encryption": {"standard"}, "filename_encoding": {"base32"}, "directory_encryption": {"on"}, "suffix": {".bin"}, "new_crypt_prefix": {"on"}}
	values.Set("bucket", "invalid bucket")
	out = request("POST", "/notebooks", testutil.Owner, "notebook-feedback", values)
	if out.Code != 422 || out.Header().Get("HX-Retarget") != "#notebook-feedback" || strings.Contains(out.Body.String(), "sentinel") || strings.Contains(out.Body.String(), "<form") {
		t.Fatal("validation discarded draft", out.Code, out.Body.String())
	}
	values.Set("bucket", "test")
	out = request("POST", "/notebooks", testutil.Owner, "notebook-feedback", values)
	if out.Code != 200 || !strings.Contains(out.Header().Get("HX-Location"), `"path":"/notebooks"`) {
		t.Fatal("create", out.Code, out.Body.String())
	}
	books, err := repo.Notebooks(ctx, testutil.Owner)
	if err != nil || len(books) != 1 {
		t.Fatal(books, err)
	}
	n := books[0]
	out = request("GET", "/notebooks", testutil.Owner, "", nil)
	for _, want := range []string{`id="add-notebook"`, "Encrypted notes", "Read-only access is set by the connection.", "/recovery/" + n.SecretID} {
		if !strings.Contains(out.Body.String(), want) {
			t.Fatal("list lost", want)
		}
	}
	if strings.Contains(out.Body.String(), "data-notebook-form") {
		t.Fatal("creation form on populated list")
	}
	out = request("GET", "/storage", testutil.Owner, "", nil)
	if strings.Contains(out.Body.String(), "Encrypted notes") || strings.Contains(out.Body.String(), "data-notebook-form") {
		t.Fatal("notebooks still on connections page")
	}
	for _, path := range []string{"/notebooks?notebook=" + n.ID, "/storage?notebook=" + n.ID} {
		out = request("GET", path, testutil.Owner, "", nil)
		if out.Code != 200 || !strings.Contains(out.Body.String(), `name="revision" value="1"`) {
			t.Fatal("fallback", out.Code, out.Body.String())
		}
		out = request("GET", path, testutil.Other, "notebook-dialog-content", nil)
		if out.Code != 404 || out.Header().Get("HX-Retarget") != "#notebook-list-feedback" {
			t.Fatal("owner isolation", out.Code, out.Body.String())
		}
	}
	out = request("GET", "/notebooks?notebook="+n.ID, testutil.Owner, "notebook-dialog-content", nil)
	if out.Code != 200 || strings.Contains(out.Body.String(), "<html") || strings.Contains(out.Body.String(), "sentinel") || !strings.Contains(out.Body.String(), `data-close-notebook`) {
		t.Fatal("edit fragment", out.Code, out.Body.String())
	}
	edit := url.Values{"name": {"Renamed notebook"}, "revision": {fmt.Sprint(n.Revision)}, "bucket": {"other-bucket"}, "prefix": {"changed"}, "connection_id": {"other-id"}, "encrypted": {"off"}}
	out = request("POST", "/notebooks/"+n.ID, testutil.Other, "notebook-feedback", edit)
	if out.Code != 404 {
		t.Fatal("foreign update", out.Code)
	}
	out = request("POST", "/notebooks/"+n.ID, testutil.Owner, "notebook-feedback", edit)
	if out.Code != 200 || out.Header().Get("HX-Location") == "" {
		t.Fatal("update", out.Code, out.Body.String())
	}
	saved, err := repo.Notebook(ctx, testutil.Owner, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != "Renamed notebook" || saved.ConnectionID != n.ConnectionID || saved.Prefix != n.Prefix || saved.Bucket != n.Bucket || !saved.Encrypted || saved.SecretID != n.SecretID {
		t.Fatal("immutable settings changed", saved)
	}
	out = request("POST", "/notebooks/"+n.ID, testutil.Owner, "notebook-feedback", edit)
	if out.Code != 409 || out.Header().Get("HX-Retarget") != "#notebook-feedback" || strings.Contains(out.Body.String(), "<form") {
		t.Fatal("stale edit", out.Code, out.Body.String())
	}
	edit.Set("revision", fmt.Sprint(saved.Revision))
	out = request("POST", "/notebooks/"+n.ID, testutil.Owner, "", edit)
	if out.Code != 303 || out.Header().Get("Location") != "/notebooks" {
		t.Fatal("normal form redirect", out.Code)
	}
	saved, err = repo.Notebook(ctx, testutil.Owner, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	out = request("POST", "/notebooks/"+n.ID+"/delete", testutil.Owner, "", url.Values{"confirm": {"on"}, "revision": {fmt.Sprint(saved.Revision)}})
	if out.Code != 303 || out.Header().Get("Location") != "/notebooks" {
		t.Fatal("remove redirect", out.Code)
	}
	out = request("GET", "/notebooks", testutil.Owner, "", nil)
	if !strings.Contains(out.Body.String(), "data-notebook-form") || !strings.Contains(out.Body.String(), "/recovery/"+n.SecretID) {
		t.Fatal("empty state lost recovery", out.Body.String())
	}
	out = request("GET", "/recovery/"+n.SecretID, testutil.Owner, "", nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "submitted-crypt-sentinel") {
		t.Fatal("recovery lost", out.Code)
	}
}
