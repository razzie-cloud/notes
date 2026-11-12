package storage_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func TestSharedManagedStoragePrefixesAndPolicy(t *testing.T) {
	ctx := context.Background()
	admin := store.WithAdmin(ctx, true)
	pool := testutil.Database(t)
	fixture, cfg := testutil.ObjectStore(t)
	repo := &store.Repository{Pool: pool}
	registry := &storage.Registry{Repo: repo, Config: cfg}
	for _, prefix := range []bool{false, true} {
		for _, encrypted := range []bool{false, true} {
			name := fmt.Sprintf("prefix-%t-encrypted-%t", prefix, encrypted)
			t.Run(name, func(t *testing.T) {
				c := store.Connection{Owner: "33333333-3333-4333-8333-333333333333", Name: name, Provider: "Other", Endpoint: fixture.Server.URL, Region: "us-east-1", PathStyle: true, ConditionalWrites: true, Bucket: "test", Shared: true, UserIDPrefix: prefix, AccessKey: "test", SecretKey: "test"}
				if err := repo.SaveConnection(admin, &c); err != nil {
					t.Fatal(err)
				}
				books := []store.Notebook{}
				handles := []storage.Store{}
				for _, owner := range []string{testutil.Owner, testutil.Other} {
					n := store.Notebook{Owner: owner, ConnectionID: c.ID, Name: name, Bucket: "test", Prefix: name, Encrypted: encrypted, FilenameEncryption: "standard", FilenameEncoding: "base32", DirectoryEncryption: true, Suffix: ".bin"}
					if err := repo.SaveNotebook(ctx, &n, store.Secrets{Password: "compatible shared fixture password"}); err != nil {
						t.Fatal(err)
					}
					handle, _, err := registry.Open(ctx, owner, n.ID)
					if err != nil {
						t.Fatal(err)
					}
					key := owner + "/notes/entry.md"
					etag := "*"
					if !prefix && len(handles) > 0 {
						data, meta, err := handle.Get(ctx, key, "", 4096)
						if err != nil || string(data) != testutil.Owner {
							t.Fatal("unprefixed objects should be shared", err)
						}
						etag = meta.ETag
					}
					if _, err := handle.Put(ctx, key, []byte(owner), etag, "text/markdown"); err != nil {
						t.Fatal(err)
					}
					physicalPrefix := c.StoragePrefix(owner, n.Prefix) + "/"
					page, err := fixture.Raw.List(ctx, physicalPrefix, "")
					if err != nil || len(page.Objects) != 1 || !strings.HasPrefix(page.Objects[0].Key, physicalPrefix) {
						t.Fatal("incorrect physical root", page, err)
					}
					if encrypted && strings.Contains(page.Objects[0].Key, "entry.md") {
						t.Fatal("plaintext crypt name")
					}
					books = append(books, n)
					handles = append(handles, handle)
				}
				data, _, err := handles[0].Get(ctx, testutil.Owner+"/notes/entry.md", "", 4096)
				expected := testutil.Other
				if prefix {
					expected = testutil.Owner
				}
				if err != nil || string(data) != expected {
					t.Fatal("user prefix isolation", string(data), err)
				}
				// Connection changes affect every user's already-open handles.
				c.ReadOnly = true
				c.RetainSession = true
				if err := repo.SaveConnection(admin, &c); err != nil {
					t.Fatal(err)
				}
				for i, n := range books {
					if _, err := handles[i].Put(ctx, n.Owner+"/notes/blocked.md", nil, "*", "text/markdown"); !errors.Is(err, store.ErrReadOnly) {
						t.Fatal("cross-owner policy bypass", err)
					}
				}
				// Validator scheme switches invalidate compatibility tokens for all users.
				c.ReadOnly = false
				c.ConditionalWrites = false
				if err := repo.SaveConnection(admin, &c); err != nil {
					t.Fatal(err)
				}
				for i, n := range books {
					if _, _, err := handles[i].Get(ctx, n.Owner+"/notes/entry.md", "", 4096); err != nil {
						t.Fatal(err)
					}
				}
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_write_versions WHERE scope IN (SELECT id FROM notebooks WHERE connection_id=$1)`, c.ID).Scan(&count); err != nil || count != 2 {
					t.Fatal("compatibility revisions not recorded", count, err)
				}
				c.ConditionalWrites = true
				if err := repo.SaveConnection(admin, &c); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_write_versions WHERE scope IN (SELECT id FROM notebooks WHERE connection_id=$1)`, c.ID).Scan(&count); err != nil || count != 0 {
					t.Fatal("cross-owner revisions were not invalidated", count, err)
				}
			})
		}
	}
}
