package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/razzie-cloud/notes/internal/app"
	"github.com/razzie-cloud/notes/internal/index"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func TestAuthorizationBoundary(t *testing.T) {
	a, err := app.New(structConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	for _, tc := range []struct {
		method, path, owner, origin, admin string
		status                             int
	}{{"GET", "/v1/notes", "", "", "", 401}, {"GET", "/v1/notes", "not-uuid", "", "", 401}, {"POST", "/v1/admin/rebuild", testutil.Owner, "http://evil", "true", 403}, {"GET", "/v1/admin/index", testutil.Owner, "", "", 403}, {"POST", "/v1/admin/rebuild", testutil.Owner, "null", "true", 403}, {"GET", "/healthz", "", "", "", 200}, {"GET", "/readyz", "", "", "", 503}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.owner != "" {
			r.Header.Set("X-Auth-Request-User", tc.owner)
		}
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Notes-Admin", tc.admin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
}

func TestAPIAndSearchIsolation(t *testing.T) {
	pool := testutil.Database(t)
	s, c := testutil.ObjectStore(t)
	registry, book := testutil.Attach(t, pool, s, c, testutil.Owner, "data", false, "")
	_, otherBook := testutil.Attach(t, pool, s, c, testutil.Other, "other", false, "")
	root, err := index.New(pool, registry, c)
	if err != nil {
		t.Fatal(err)
	}
	e, err := root.Bind(context.Background(), testutil.Owner, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(c, e)
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	request := func(method, path, body, user, etag string) *httptest.ResponseRecorder {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		id := book.ID
		if user == testutil.Other {
			id = otherBook.ID
		}
		path += sep + "notebook=" + id
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Auth-Request-User", user)
		if etag == "*" {
			r.Header.Set("If-None-Match", "*")
		} else if etag != "" {
			r.Header.Set("If-Match", etag)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	content := "---\ntitle: Árvíztűrő\ntags: [magyar]\n---\nTükörfúrógép őrzött jegyzet"
	w := request("PUT", "/v1/notes/a.md", content, testutil.Owner, "*")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	etag := w.Header().Get("ETag")
	if w = request("PUT", "/v1/notes/a.md", "# overwrite", testutil.Owner, "*"); w.Code != 412 || w.Header().Get("ETag") != etag {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	if w = request("PUT", "/v1/notes/a.md", "# overwrite", testutil.Owner, ""); w.Code != 428 {
		t.Fatal(w.Code)
	}
	if w = request("GET", "/v1/notes/a.md", "", testutil.Other, ""); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = e.Index(context.Background(), testutil.Owner+"/notes/a.md"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?q=tükörfúrógép", "?tag=magyar", "?q=őrzött"} {
		w = request("GET", "/v1/notes"+query, "", testutil.Owner, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "Árvíztűrő") {
			t.Fatal(query, w.Code, w.Body.String())
		}
		w = request("GET", "/v1/notes"+query, "", testutil.Other, "")
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
	w = request("GET", "/v1/notes?tag=missing", "", testutil.Owner, "")
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Body.String())
	}
	w = request("GET", "/edit?key=a.md", "", testutil.Owner, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Markdown editor") || w.Header().Get("Referrer-Policy") != "same-origin" || !strings.Contains(w.Body.String(), `for="note-content"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/", "", testutil.Owner, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Árvíztűrő") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("PUT", "/v1/attachments/a.md/test.html", "<script>evil</script>", testutil.Owner, "*")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = e.Index(context.Background(), testutil.Owner+"/attachments/a.md/test.html"); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/v1/attachments/a.md/test.html", "", testutil.Owner, "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal(w.Code, w.Header())
	}
	w = request("GET", "/v1/attachments?note=a.md", "", testutil.Other, "")
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal(w.Body.String())
	}
	w = request("DELETE", "/v1/notes/a.md", "", testutil.Owner, etag)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = e.Index(context.Background(), testutil.Owner+"/notes/a.md"); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/v1/notes/a.md", "", testutil.Owner, "")
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	// An S3 success remains successful if the transactional queue is unavailable.
	if _, err = pool.Exec(context.Background(), `ALTER TABLE river_job RENAME TO river_job_offline`); err != nil {
		t.Fatal(err)
	}
	w = request("PUT", "/v1/notes/repair.md", "# Repair later", testutil.Owner, "*")
	if w.Code != 201 {
		t.Fatal("S3 success reported failed", w.Code, w.Body.String())
	}
	if _, err = pool.Exec(context.Background(), `ALTER TABLE river_job_offline RENAME TO river_job`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		done, err := e.ScanPage(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
	if err = e.Index(context.Background(), testutil.Owner+"/notes/repair.md"); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/v1/notes?q=repair", "", testutil.Owner, "")
	if !strings.Contains(w.Body.String(), "Repair later") {
		t.Fatal(w.Code, w.Body.String())
	}

	// Admin marker is supplied only by the proxy after gateway admin validation.
	r := httptest.NewRequest(http.MethodPost, "/v1/admin/rebuild?notebook="+book.ID, nil)
	r.Header.Set("X-Auth-Request-User", testutil.Owner)
	r.Header.Set("X-Notes-Admin", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 202 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	r = httptest.NewRequest("GET", "/v1/admin/index?notebook="+book.ID, nil)
	r.Header.Set("X-Auth-Request-User", testutil.Owner)
	r.Header.Set("X-Notes-Admin", "true")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// HTML source is always escaped; the UI never executes imported Markdown.
	w = request("PUT", "/v1/notes/xss.md", "# <script>alert(1)</script>", testutil.Owner, "*")
	if w.Code != 201 {
		t.Fatal(w.Code)
	}
	w = request("GET", "/edit?key=xss.md", "", testutil.Owner, "")
	if strings.Contains(w.Body.String(), "<script>alert(1)") {
		t.Fatal("unescaped draft")
	}
}

func TestFailedEditorSavePreservesDraftAndLogsSafeDetails(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	s, cfg := testutil.ObjectStore(t)
	registry, book := testutil.Attach(t, pool, s, cfg, testutil.Owner, "save-failure", false, "")
	engine, err := index.New(pool, registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(cfg, engine)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	s.FailPut.Store(true)
	form := url.Values{"key": {"private-note.md"}, "content": {"# My private draft"}, "etag": {"*"}}
	r := httptest.NewRequest("POST", "/save?notebook="+book.ID, strings.NewReader(form.Encode())).WithContext(ctx)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Auth-Request-User", testutil.Owner)
	r.Header.Set("Origin", cfg.Origin)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 503 || w.Header().Get("X-Request-ID") == "" || !strings.Contains(w.Body.String(), "My private draft") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	output := logs.String()
	for _, wanted := range []string{`"event":"notes.request_failed"`, `"operation":"put_object"`, `"s3_code":"InvalidArgument"`, `"upstream_status":400`, w.Header().Get("X-Request-ID")} {
		if !strings.Contains(output, wanted) {
			t.Fatal("missing diagnostic", wanted, output)
		}
	}
	for _, secret := range []string{"private-note.md", "My private draft", "private note path and credential"} {
		if strings.Contains(output, secret) {
			t.Fatal("secret in log", secret)
		}
	}
}
