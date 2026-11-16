package app

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func checkFirstUse(t *testing.T, body string, available bool) {
	t.Helper()
	want, avoid := "Add a connection", "Add your first notebook"
	if available {
		want, avoid = avoid, want
	}
	if !strings.Contains(body, "<h2>"+want+"</h2>") || strings.Contains(body, "<h2>"+avoid+"</h2>") {
		t.Fatalf("available=%v: incorrect onboarding: %s", available, body)
	}
	if available && !strings.Contains(body, `href="/notebooks" hx-boost="true" hx-target="#app-layout"`) {
		t.Fatal("first notebook action lost its direct URL or HTMX navigation")
	}
}

func TestFirstUsePresentation(t *testing.T) {
	a, err := New(config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		connections []store.Connection
	}{
		{name: "none"},
		{name: "personal", connections: []store.Connection{{ID: "personal"}}},
		{name: "shared", connections: []store.Connection{{ID: "shared", Shared: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := a.templates.ExecuteTemplate(&out, "page", View{StorageNeeded: true, Connections: tc.connections}); err != nil {
				t.Fatal(err)
			}
			checkFirstUse(t, out.String(), len(tc.connections) > 0)
		})
	}
}

func TestFirstUseAvailableConnections(t *testing.T) {
	repo := &store.Repository{Pool: testutil.Database(t)}
	a, err := New(config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.Repo = repo
	handler := a.Handler()
	request := func(available bool) {
		t.Helper()
		for _, fragment := range []bool{false, true} {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("X-Auth-Request-User", testutil.Owner)
			if fragment {
				r.Header.Set("HX-Request", "true")
				r.Header.Set("HX-Target", "app-layout")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("fragment=%v: %d %s", fragment, w.Code, w.Body.String())
			}
			checkFirstUse(t, w.Body.String(), available)
		}
	}
	request(false)
	ctx := context.Background()
	c := store.Connection{Owner: testutil.Other, Name: "Other user's personal connection", Provider: "AWS", Region: "us-east-1", AccessKey: "access", SecretKey: "secret"}
	if err := repo.SaveConnection(ctx, &c); err != nil {
		t.Fatal(err)
	}
	request(false)
	shared := c
	shared.ID, shared.Name, shared.Shared = "", "Shared connection", true
	if err := repo.SaveConnection(store.WithAdmin(ctx, true), &shared); err != nil {
		t.Fatal(err)
	}
	request(true)
	if err := repo.RemoveConnection(store.WithAdmin(ctx, true), testutil.Other, shared.ID, shared.Revision); err != nil {
		t.Fatal(err)
	}
	request(false)
	c.ID, c.Owner, c.Name = "", testutil.Owner, "Own personal connection"
	if err := repo.SaveConnection(ctx, &c); err != nil {
		t.Fatal(err)
	}
	request(true)
}
