package app

import (
	"context"
	"fmt"
	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestConnectionDialogWorkflow(t *testing.T) {
	repo := &store.Repository{Pool: testutil.Database(t)}
	app, err := New(config.Config{Origin: "http://example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	app.Repo = repo
	handler := app.Handler()
	owner := "11111111-1111-4111-8111-111111111111"
	other := "22222222-2222-4222-8222-222222222222"
	request := func(method, target, user, hxTarget string, values url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(values.Encode()))
		req.Header.Set("X-Auth-Request-User", user)
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if hxTarget != "" {
			req.Header.Set("HX-Request", "true")
			req.Header.Set("HX-Target", hxTarget)
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	out := request("GET", "/storage", owner, "", nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "data-connection-form") {
		t.Fatalf("first use: %d %s", out.Code, out.Body.String())
	}
	values := url.Values{"name": {"Browser storage"}, "provider": {"AWS"}, "region": {"us-east-1"}, "path_style": {"on"}, "conditional_writes": {"on"}, "access_mode": {"writable"}, "access_key": {"stored-access"}, "secret_key": {"stored-secret"}, "session_token": {"stored-token"}}
	out = request("POST", "/connections", owner, "connection-feedback", values)
	if out.Code != 200 || !strings.Contains(out.Header().Get("HX-Location"), "/storage") {
		t.Fatalf("create: %d %s", out.Code, out.Body.String())
	}
	all, err := repo.Connections(context.Background(), owner)
	if err != nil || len(all) != 1 {
		t.Fatal(all, err)
	}
	c := all[0]
	editURL := "/storage?connection=" + c.ID
	updateURL := "/connections/" + c.ID
	out = request("GET", "/storage", owner, "", nil)
	if strings.Contains(out.Body.String(), "data-connection-form") || !strings.Contains(out.Body.String(), `id="add-connection"`) {
		t.Fatal("populated list still shows the form")
	}
	out = request("GET", editURL, owner, "connection-dialog-content", nil)
	if out.Code != 200 || strings.Contains(out.Body.String(), "<html") || !strings.Contains(out.Body.String(), `value="Browser storage"`) || !strings.Contains(out.Body.String(), `data-close-connection`) {
		t.Fatalf("edit: %d %s", out.Code, out.Body.String())
	}
	for _, secret := range []string{"stored-access", "stored-secret", "stored-token"} {
		if strings.Contains(out.Body.String(), secret) {
			t.Fatal("saved credential leaked")
		}
	}
	out = request("GET", editURL, other, "connection-dialog-content", nil)
	if out.Code != 404 || out.Header().Get("HX-Retarget") != "#connection-list-feedback" || strings.Contains(out.Body.String(), "Browser storage") {
		t.Fatalf("owner scope: %d %s", out.Code, out.Body.String())
	}
	values.Set("revision", fmt.Sprint(c.Revision))
	values.Set("name", "")
	values.Set("secret_key", "submitted-secret-sentinel")
	out = request("POST", updateURL, owner, "connection-feedback", values)
	if out.Code != 422 || out.Header().Get("HX-Retarget") != "#connection-feedback" || strings.Contains(out.Body.String(), "submitted-secret-sentinel") || strings.Contains(out.Body.String(), "<form") {
		t.Fatalf("validation: %d %s", out.Code, out.Body.String())
	}
	values.Set("name", "Renamed storage")
	values.Set("access_key", "")
	values.Set("secret_key", "")
	values.Set("session_token", "")
	out = request("POST", updateURL, owner, "connection-feedback", values)
	if out.Code != 200 || out.Header().Get("HX-Location") == "" {
		t.Fatalf("update: %d %s", out.Code, out.Body.String())
	}
	credentials, err := repo.Credentials(context.Background(), owner, c.ID)
	if err != nil || credentials.AccessKey != "stored-access" || credentials.SecretKey != "stored-secret" || credentials.SessionToken != "stored-token" {
		t.Fatal("blank credentials were not retained", err)
	}
	out = request("POST", updateURL, owner, "connection-feedback", values)
	if out.Code != 409 || out.Header().Get("HX-Retarget") != "#connection-feedback" {
		t.Fatalf("stale revision: %d %s", out.Code, out.Body.String())
	}
	out = request("GET", editURL, owner, "", nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "<html") || !strings.Contains(out.Body.String(), "data-connection-form") {
		t.Fatal("full-page edit fallback missing")
	}
	updated, err := repo.Connection(context.Background(), owner, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	book := store.Notebook{Owner: owner, ConnectionID: c.ID, ConnectionRevision: updated.Revision, Name: "Personal", Bucket: "notes", Prefix: "personal", FilenameEncryption: "standard", FilenameEncoding: "base32", DirectoryEncryption: true, Suffix: ".bin"}
	if err := repo.SaveNotebook(context.Background(), &book, store.Secrets{}); err != nil {
		t.Fatal(err)
	}
	out = request("GET", editURL, owner, "connection-dialog-content", nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `<fieldset disabled`) {
		t.Fatal("attached notebook did not lock location fields", out.Code, out.Body.String())
	}

}
