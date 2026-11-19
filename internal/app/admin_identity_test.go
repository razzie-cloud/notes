package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/razzie-cloud/notes/internal/index"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func TestSignedAdminNotebookAPIs(t *testing.T) {
	pool := testutil.Database(t)
	s3, cfg := testutil.ObjectStore(t)
	registry, book := testutil.Attach(t, pool, s3, cfg, testutil.Owner, "admin", false, "")
	// Index maintenance must also work for a read-only notebook.
	book.ReadOnly = true
	if err := registry.Repo.SaveNotebook(t.Context(), &book, store.Secrets{}); err != nil {
		t.Fatal(err)
	}
	engine, err := index.New(pool, registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	gateway, sign := sharedGateway(t, cfg.Origin)
	cfg.AccessGatewayURL = gateway
	server, err := New(cfg, engine)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	for _, endpoint := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/v1/admin/index?notebook=", http.StatusOK},
		{http.MethodPost, "/v1/admin/rebuild?notebook=", http.StatusAccepted},
		{http.MethodPost, "/v1/admin/rebuild?restart=true&notebook=", http.StatusAccepted},
	} {
		for _, actor := range []struct {
			name, owner string
			admin       bool
			status      int
		}{
			{"member-with-forged-marker", testutil.Owner, false, http.StatusForbidden},
			{"admin-without-marker", testutil.Owner, true, endpoint.status},
			{"admin-other-owner", testutil.Other, true, http.StatusNotFound},
		} {
			t.Run(endpoint.method+" "+endpoint.path+"/"+actor.name, func(t *testing.T) {
				r := httptest.NewRequest(endpoint.method, endpoint.path+book.ID, nil)
				sign(r, actor.owner, actor.admin)
				if !actor.admin {
					r.Header.Set("X-Notes-Admin", "true")
					r.Header.Set("X-Auth-Request-Admin", "true")
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != actor.status {
					t.Fatalf("got %d want %d: %s", w.Code, actor.status, w.Body.String())
				}
			})
		}
	}
}
