package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/razzie-cloud/notes/internal/index"
	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestFixedBucketPolicyAndConnectionDefaults(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint("shared=", shared), func(t *testing.T) {
			const origin = "http://example.com"
			const admin = "11111111-1111-4111-8111-111111111111"
			const member = "22222222-2222-4222-8222-222222222222"
			ctx := context.Background()
			gateway, sign := sharedGateway(t, origin)
			repo := &store.Repository{Pool: testutil.Database(t)}
			fixture, cfg := testutil.ObjectStore(t)
			cfg.Origin = origin
			cfg.AccessGatewayURL = gateway
			registry := &storage.Registry{Repo: repo, Config: cfg}
			engine, err := index.New(repo.Pool, registry, cfg)
			if err != nil {
				t.Fatal(err)
			}
			a, err := New(cfg, engine)
			endpoint := fixture.Server.URL
			if err != nil {
				t.Fatal(err)
			}
			handler := a.Handler()
			request := func(method, path, actor string, isAdmin bool, values url.Values) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
				r.Header.Set("Origin", origin)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				sign(r, actor, isAdmin)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			actor := member
			if shared {
				actor = admin
			}
			out := request("GET", "/storage?connection=new", actor, shared, nil)
			field := regexp.MustCompile(`<input[^>]*name="path_style"[^>]*>`).FindString(out.Body.String())
			if out.Code != 200 || field == "" || strings.Contains(field, "checked") {
				t.Fatal("new connections must default path style off", out.Code, field)
			}
			values := url.Values{"name": {"Fixed storage"}, "provider": {"Other"}, "endpoint": {endpoint}, "region": {"us-east-1"}, "access_key": {"test"}, "secret_key": {"test-secret"}, "bucket": {"test"}, "access_mode": {"writable"}, "conditional_writes": {"on"}}
			if shared {
				values.Set("shared", "on")
			}
			if out := request("POST", "/connections", actor, shared, values); out.Code != 303 {
				t.Fatal("connection create", out.Code, out.Body.String())
			}
			all, err := repo.Connections(ctx, member)
			if err != nil || len(all) != 1 {
				t.Fatal(all, err)
			}
			c := all[0]
			if c.Bucket != "test" || c.PathStyle {
				t.Fatal("bucket/addressing not persisted", c)
			}
			out = request("GET", "/storage", member, false, nil)
			if !strings.Contains(out.Body.String(), ">Use connection</a>") {
				t.Fatal("missing Use connection for personal/shared connection")
			}
			out = request("GET", "/notebooks?notebook=new&connection="+c.ID, member, false, nil)
			bucketInput := regexp.MustCompile(`<input[^>]*name="bucket"[^>]*>`).FindString(out.Body.String())
			if out.Code != 200 || !strings.Contains(bucketInput, `value="test"`) || !strings.Contains(bucketInput, "readonly") {
				t.Fatal("fixed bucket not prefilled/read-only", out.Code, bucketInput)
			}
			// The local S3 fixture needs explicit path-style addressing; existing saved
			// choices must stay checked while new connections default off.
			values.Set("revision", fmt.Sprint(c.Revision))
			values.Set("path_style", "on")
			update := "/connections/" + c.ID
			if out := request("POST", update, actor, shared, values); out.Code != 303 {
				t.Fatal("path-style opt-in", out.Code, out.Body.String())
			}
			c, err = repo.Connection(ctx, member, c.ID)
			if err != nil || !c.PathStyle {
				t.Fatal(c, err)
			}
			form := url.Values{"name": {"Fixed area"}, "connection_id": {c.ID}, "prefix": {"projects"}, "encryption_mode": {"none"}, "bucket": {"other-bucket"}}
			if out := request("POST", "/notebooks", member, false, form); out.Code != 422 {
				t.Fatal("forged bucket accepted", out.Code, out.Body.String())
			}
			// An omitted bucket resolves to the configured bucket on the server too.
			form.Del("bucket")
			if out := request("POST", "/notebooks", member, false, form); out.Code != 303 {
				t.Fatal("fixed mapping create", out.Code, out.Body.String())
			}
			allMaps, err := repo.Notebooks(ctx, member)
			if err != nil || len(allMaps) != 1 || allMaps[0].Bucket != "test" {
				t.Fatal("fixed bucket not saved", allMaps, err)
			}
			mapping := allMaps[0]
			forged := mapping
			forged.ID = ""
			forged.Bucket = "other-bucket"
			forged.Name = "Forged repository"
			if err := repo.SaveNotebook(ctx, &forged, store.Secrets{}); !errors.Is(err, store.ErrBucket) {
				t.Fatal("repository bucket bypass", err)
			}
			if _, err := repo.Pool.Exec(ctx, `UPDATE notebooks SET bucket='other-bucket' WHERE id=$1`, mapping.ID); err == nil {
				t.Fatal("SQL bucket bypass")
			}
			values.Set("revision", fmt.Sprint(c.Revision))
			values.Set("bucket", "other-bucket")
			if out := request("POST", update, actor, shared, values); out.Code != 409 {
				t.Fatal("incompatible restriction changed", out.Code, out.Body.String())
			}
			values.Set("bucket", "")
			if out := request("POST", update, actor, shared, values); out.Code != 303 {
				t.Fatal("clear restriction", out.Code, out.Body.String())
			}
			c, err = repo.Connection(ctx, member, c.ID)
			if err != nil || c.Bucket != "" {
				t.Fatal(c, err)
			}
			values.Set("revision", fmt.Sprint(c.Revision))
			values.Set("bucket", "test")
			if out := request("POST", update, actor, shared, values); out.Code != 303 {
				t.Fatal("compatible existing restriction", out.Code, out.Body.String())
			}
		})
	}
}
