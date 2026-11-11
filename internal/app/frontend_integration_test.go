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

func TestFrontendNotesAndDrafts(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	fixture, cfg := testutil.ObjectStore(t)
	registry, book := testutil.Attach(t, pool, fixture, cfg, testutil.Owner, "frontend", false, "")
	root, err := index.New(pool, registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := root.Bind(ctx, testutil.Owner, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := app.New(cfg, root)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	request := func(method, route, user string, form url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, route, strings.NewReader(form.Encode()))
		r.Header.Set("X-Auth-Request-User", user)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", cfg.Origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	original := "---\nid: preserved\ntitle: Field notes\ntags: [homelab]\ncustom: keep-me\n---\n# Heading\n\n**Important** storage notes."
	key := testutil.Owner + "/notes/field.md"
	meta, err := engine.Storage.Put(ctx, key, []byte(original), "*", "text/markdown")
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.Index(ctx, key); err != nil {
		t.Fatal(err)
	}
	suffix := "?notebook=" + book.ID
	for _, route := range []string{"/" + suffix, "/tags" + suffix, "/note" + suffix + "&key=field.md", "/edit" + suffix + "&key=field.md", "/" + suffix + "&tag=homelab"} {
		w := request("GET", route, testutil.Owner, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `class="sidebar"`) || !strings.Contains(w.Body.String(), "homelab") {
			t.Fatal(route, w.Code, w.Body.String())
		}
		foreign := request("GET", route, testutil.Other, nil)
		if foreign.Code != 404 {
			t.Fatal("owner isolation", route, foreign.Code)
		}
	}
	read := request("GET", "/note"+suffix+"&key=field.md", testutil.Owner, nil)
	if !strings.Contains(read.Body.String(), "<strong>Important</strong>") || strings.Contains(read.Body.String(), "custom: keep-me") {
		t.Fatal(read.Body.String())
	}
	preview := request("POST", "/preview"+suffix, testutil.Owner, url.Values{"content": {"# Preview\n\n<script>bad()</script>\n\n![x](https://tracker.invalid)"}})
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "<h2>Preview</h2>") || strings.Contains(preview.Body.String(), "<script>") || strings.Contains(preview.Body.String(), "<img") {
		t.Fatal(preview.Code, preview.Body.String())
	}
	stored, _, err := engine.Storage.Get(ctx, key, "", cfg.MaxNoteBytes)
	if err != nil || string(stored) != original {
		t.Fatal("preview mutated storage", err)
	}
	form := url.Values{"metadata": {"on"}, "key": {"field.md"}, "etag": {meta.Validator()}, "front_matter": {"id: preserved\ncustom: keep-me"}, "title": {"Revised title"}, "tags": {"magyar, homelab"}, "content": {"## My draft\n\nKeep me."}}
	saved := request("POST", "/save"+suffix, testutil.Owner, form)
	if saved.Code != 200 || !strings.Contains(saved.Body.String(), "Saved.") {
		t.Fatal(saved.Code, saved.Body.String())
	}
	stored, _, err = engine.Storage.Get(ctx, key, "", cfg.MaxNoteBytes)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := index.Parse("field.md", stored)
	if err != nil || doc.ID != "preserved" || doc.Title != "Revised title" || len(doc.Tags) != 2 || !strings.Contains(string(stored), "custom: keep-me") {
		t.Fatal(doc, string(stored), err)
	}
	conflict := request("POST", "/save"+suffix, testutil.Owner, form)
	for _, want := range []string{`data-failed="true"`, "Revised title", "magyar, homelab", "## My draft", "changed elsewhere"} {
		if conflict.Code != 412 || !strings.Contains(conflict.Body.String(), want) {
			t.Fatal(want, conflict.Code, conflict.Body.String())
		}
	}
	if _, err = pool.Exec(ctx, `UPDATE storage_connections SET read_only=true WHERE id=$1`, book.ConnectionID); err != nil {
		t.Fatal(err)
	}
	readonly := request("GET", "/edit"+suffix+"&key=field.md", testutil.Owner, nil)
	if readonly.Code != 200 || strings.Contains(readonly.Body.String(), `form="editor"`) || !strings.Contains(readonly.Body.String(), "This storage is read-only.") {
		t.Fatal(readonly.Code, readonly.Body.String())
	}
	empty := request("GET", "/"+suffix+"&q=missingword", testutil.Owner, nil)
	if strings.Contains(empty.Body.String(), ">Create a note</a>") || strings.Contains(empty.Body.String(), ">New note</a>") {
		t.Fatal("read-only action", empty.Body.String())
	}
	if w := request("POST", "/save"+suffix, testutil.Owner, form); w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
}

func TestFrontendAttachmentValidators(t *testing.T) {
	for _, strict := range []bool{true, false} {
		name := "S3"
		if !strict {
			name = "database"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			pool := testutil.Database(t)
			fixture, cfg := testutil.ObjectStore(t)
			registry, book := testutil.Attach(t, pool, fixture, cfg, testutil.Owner, "attachment-ui", false, "")
			if _, err := pool.Exec(ctx, `UPDATE storage_connections SET conditional_writes=$1 WHERE id=$2`, strict, book.ConnectionID); err != nil {
				t.Fatal(err)
			}
			fixture.RejectConditionalWrites.Store(!strict)
			root, err := index.New(pool, registry, cfg)
			if err != nil {
				t.Fatal(err)
			}
			engine, err := root.Bind(ctx, testutil.Owner, book.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = engine.Storage.Put(ctx, testutil.Owner+"/notes/a.md", []byte("# Note"), "*", "text/markdown"); err != nil {
				t.Fatal(err)
			}
			key := testutil.Owner + "/attachments/a.md/file.txt"
			old, err := engine.Storage.Put(ctx, key, []byte("Before"), "*", "application/octet-stream")
			if err != nil {
				t.Fatal(err)
			}
			if err = engine.Index(ctx, key); err != nil {
				t.Fatal(err)
			}
			server, err := app.New(cfg, root)
			if err != nil {
				t.Fatal(err)
			}
			request := func(method, route, user string, form url.Values) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, route, strings.NewReader(form.Encode()))
				r.Header.Set("X-Auth-Request-User", user)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("Origin", cfg.Origin)
				w := httptest.NewRecorder()
				server.Handler().ServeHTTP(w, r)
				return w
			}
			suffix := "?notebook=" + book.ID
			if _, err = pool.Exec(ctx, `DELETE FROM objects WHERE scope=$1 AND kind='attachments'`, book.ID); err != nil {
				t.Fatal(err)
			}
			justUploaded := request("GET", "/edit"+suffix+"&key=a.md&uploaded=file.txt", testutil.Owner, nil)
			if justUploaded.Code != 200 || !strings.Contains(justUploaded.Body.String(), "Remove file.txt") {
				t.Fatal("uploaded attachment missing before index catches up", justUploaded.Code, justUploaded.Body.String())
			}
			if err = engine.Index(ctx, key); err != nil {
				t.Fatal(err)
			}
			editor := request("GET", "/edit"+suffix+"&key=a.md", testutil.Owner, nil)
			if editor.Code != 200 || !strings.Contains(editor.Body.String(), html.EscapeString(old.Validator())) {
				t.Fatal("editor must issue current write tokens", editor.Code, editor.Body.String())
			}
			fresh, err := engine.Storage.Put(ctx, key, []byte("After"), old.Validator(), "application/octet-stream")
			if err != nil {
				t.Fatal(err)
			}
			form := url.Values{"note": {"a.md"}, "key": {"a.md/file.txt"}, "etag": {old.Validator()}}
			if w := request("POST", "/attachments/delete"+suffix, testutil.Owner, form); w.Code != 412 {
				t.Fatal("stale removal accepted", w.Code, w.Body.String())
			}
			form.Set("etag", fresh.Validator())
			if w := request("POST", "/attachments/delete"+suffix, testutil.Other, form); w.Code != 404 {
				t.Fatal("foreign removal", w.Code)
			}
			form.Set("note", "other.md")
			if w := request("POST", "/attachments/delete"+suffix, testutil.Owner, form); w.Code != 400 {
				t.Fatal("wrong parent accepted", w.Code)
			}
			form.Set("note", "a.md")
			if w := request("POST", "/attachments/delete"+suffix, testutil.Owner, form); w.Code != 303 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
