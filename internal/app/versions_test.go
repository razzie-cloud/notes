package app_test

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/razzie-cloud/notes/internal/app"
	"github.com/razzie-cloud/notes/internal/index"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func TestCompatibilityEditorAPIAndQueueFailure(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	fixture, cfg := testutil.ObjectStore(t)
	registry, book := testutil.Attach(t, pool, fixture, cfg, testutil.Owner, "compat", false, "")
	connection, err := registry.Repo.Connection(ctx, testutil.Owner, book.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	connection.ConditionalWrites = false
	connection.RetainSession = true
	if err = registry.Repo.SaveConnection(ctx, &connection); err != nil {
		t.Fatal(err)
	}
	fixture.RejectConditionalWrites.Store(true)
	engine, err := index.New(pool, registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(cfg, engine)
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	request := func(method, path, body, validator string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path+"?notebook="+book.ID, strings.NewReader(body))
		r.Header.Set("X-Auth-Request-User", testutil.Owner)
		if validator == "*" {
			r.Header.Set("If-None-Match", "*")
		} else {
			r.Header.Set("If-Match", validator)
		}
		if method == "POST" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", cfg.Origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request("PUT", "/v1/notes/a.md", "# First", "*")
	first := w.Header().Get("ETag")
	if w.Code != 201 || !strings.HasPrefix(first, `"notes-db-`) {
		t.Fatal(w.Code, w.Body.String(), first)
	}
	w = request("GET", "/v1/notes/a.md", "", "")
	if w.Code != 200 || w.Header().Get("ETag") != first {
		t.Fatal("read lost DB revision", w.Code, w.Header())
	}
	r := httptest.NewRequest("GET", "/edit?notebook="+book.ID+"&key=a.md", nil)
	r.Header.Set("X-Auth-Request-User", testutil.Owner)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(html.UnescapeString(w.Body.String()), `name="etag" value="`+first+`"`) {
		t.Fatal("editor did not receive DB revision", w.Code)
	}
	w = request("PUT", "/v1/notes/a.md", "# First", first)
	second := w.Header().Get("ETag")
	if w.Code != 200 || second == first {
		t.Fatal(w.Code, w.Body.String(), second)
	}
	form := url.Values{"key": {"a.md"}, "content": {"# Preserved stale draft"}, "etag": {first}}
	w = request("POST", "/save", form.Encode(), "")
	if w.Code != 412 || !strings.Contains(w.Body.String(), "Preserved stale draft") {
		t.Fatal("stale draft not preserved", w.Code, w.Body.String())
	}
	w = request("PUT", "/v1/notes/a.md", "# Stale", first)
	if w.Code != 412 || w.Header().Get("ETag") != second {
		t.Fatal("API conflict missing current validator", w.Code, w.Header())
	}
	// PostgreSQL queue errors must not roll back the write-version CAS. A
	// savepoint isolates the failed insertion from concurrency coordination.
	if _, err = pool.Exec(ctx, `ALTER TABLE river_job RENAME TO river_job_offline`); err != nil {
		t.Fatal(err)
	}
	w = request("PUT", "/v1/notes/a.md", "# First", second)
	third := w.Header().Get("ETag")
	if w.Code != 200 || third == second {
		t.Fatal("saved content reported failed", w.Code, w.Body.String())
	}
	w = request("GET", "/v1/notes/a.md", "", "")
	if w.Code != 200 || w.Header().Get("ETag") != third {
		t.Fatal("queue failure rolled back revision", w.Code, w.Header())
	}
	w = request("PUT", "/v1/notes/a.md", "# Stale", second)
	if w.Code != 412 {
		t.Fatal("old revision accepted after queue failure", w.Code)
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE river_job_offline RENAME TO river_job`); err != nil {
		t.Fatal(err)
	}
	w = request("PUT", "/v1/attachments/a.md/file.txt", "attachment", "*")
	attachment := w.Header().Get("ETag")
	if w.Code != 201 || !strings.HasPrefix(attachment, `"notes-db-`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("DELETE", "/v1/attachments/a.md/file.txt", "", attachment); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = request("DELETE", "/v1/notes/a.md", "", second); w.Code != 412 {
		t.Fatal("stale delete accepted", w.Code)
	}
	if w = request("DELETE", "/v1/notes/a.md", "", third); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
}
