package app

import (
	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/index"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountHeaderIdentityAndDestination(t *testing.T) {
	const origin = "https://notes.example"
	gateway, sign := sharedGateway(t, origin)
	signed, err := New(config.Config{Origin: origin, AccessGatewayURL: gateway}, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := New(config.Config{LandingOrigin: "http://localhost:8082"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		signed, admin bool
	}{{"signed-admin", true, true}, {"signed-member", true, false}, {"forged-legacy-admin", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := legacy
			if tc.signed {
				s = signed
			}
			r := httptest.NewRequest("GET", "https://untrusted.example/", nil)
			r.Header.Set("X-Auth-Request-Name", "<member>")
			r.Header.Set("X-Auth-Request-Admin", "true")
			if tc.signed {
				sign(r, "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", tc.admin)
				if err := s.identityVerifier.Verify(r, false); err != nil {
					t.Fatal(err)
				}
			}
			r = r.WithContext(store.WithAdmin(r.Context(), s.identityVerifier.IsAdmin(r)))

			w := httptest.NewRecorder()
			s.render(w, r, View{StorageNeeded: true}, 200)
			header := strings.Split(w.Body.String(), "</header>")[0]
			want := "http://auth.localhost:8082/"
			if tc.signed {
				want = "https://auth.example/"
			}
			if !strings.Contains(header, `href="`+want+`"`) {
				t.Fatal("wrong account destination", header)
			}
			if strings.Contains(header, ">Administrator<") != tc.admin {
				t.Fatal("role badge must use signed admin", header)
			}
			if !strings.Contains(header, `aria-label="Signed-in account"`) {
				t.Fatal("missing account identity")
			}
			if strings.Contains(header, "<member>") {
				t.Fatal("unescaped username")
			}
			if !tc.signed && !strings.Contains(header, "&lt;member&gt;") {
				t.Fatal("missing escaped username")
			}
		})
	}
}

func TestAccountHeaderUsesGatewayIssuerAcrossPages(t *testing.T) {
	pool := testutil.Database(t)
	fixture, cfg := testutil.ObjectStore(t)
	cfg.LandingOrigin = "https://home.razzie.cloud"
	gateway, sign := sharedGateway(t, cfg.Origin)
	cfg.AccessGatewayURL = gateway
	registry, book := testutil.Attach(t, pool, fixture, cfg, testutil.Owner, "account-header", false, "")
	engine, err := index.New(pool, registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(cfg, engine)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	for _, route := range []string{"/", "/tags?notebook=" + book.ID, "/edit?notebook=" + book.ID, "/storage", "/notebooks", "/storage?notebook=" + book.ID} {
		t.Run(route, func(t *testing.T) {
			r := httptest.NewRequest("GET", cfg.Origin+route, nil)
			sign(r, testutil.Owner, false)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			header := strings.Split(w.Body.String(), "</header>")[0]
			if !strings.Contains(header, `class="account-link" href="https://auth.example/"`) {
				t.Fatal("account link must use the discovered gateway issuer:", header)
			}
		})
	}
}
