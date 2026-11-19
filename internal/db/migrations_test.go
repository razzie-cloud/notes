package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/razzie-cloud/notes/internal/testutil"
)

func TestPhysicalWriteVersionsMigration(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Database(t)
	database := stdlib.OpenDB(*pool.Config().ConnConfig)
	defer database.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, database, os.DirFS("migrations"), goose.WithLogger(goose.NopLogger()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.DownTo(ctx, 5); err != nil {
		t.Fatal(err)
	}
	fixture, cfg := testutil.ObjectStore(t)
	registry, n := testutil.Attach(t, pool, fixture, cfg, testutil.Owner, "migration", true, "preserved secret")
	s, _, err := registry.Open(ctx, n.Owner, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := n.Owner + "/notes/preserved.md"
	if _, err = s.Put(ctx, key, []byte("# Preserved S3"), "*", "text/markdown"); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO objects(scope,owner,key,kind,relative_key,note_key,etag,size,modified) VALUES($1,$2,$3,'notes','preserved.md','preserved.md','opaque',14,now())`, n.ID, n.Owner, key)
	scan := uuid.NewString()
	exec(`INSERT INTO index_scans(scope,id,status) VALUES($1,$2,'listing')`, n.ID, scan)
	exec(`INSERT INTO scan_seen(scope,scan_id,key) VALUES($1,$2,$3)`, n.ID, scan, key)
	exec(`INSERT INTO river_job(kind,args,state,queue,max_attempts) VALUES('index_object','{}','available','default',25)`)
	revision := uuid.NewString()
	exec(`INSERT INTO object_write_versions(scope,owner,key,revision,present) VALUES($1,$2,$3,$4,true)`, n.ID, n.Owner, key, revision)
	// Verify the generated version-3 FK name rather than assuming the migration guessed it.
	var target string
	if err = pool.QueryRow(ctx, `SELECT confrelid::regclass::text FROM pg_constraint WHERE conrelid='object_write_versions'::regclass AND conname='object_write_versions_scope_owner_fkey'`).Scan(&target); err != nil || target != "notebooks" {
		t.Fatal(target, err)
	}
	tables := []string{"storage_connections", "notebooks", "storage_secrets", "objects", "index_scans", "scan_seen", "river_job"}
	snapshot := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, table := range tables {
			var value string
			if err := pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]')::text FROM `+table+` t`).Scan(&value); err != nil {
				t.Fatal(err)
			}
			out[table] = value
		}
		return out
	}
	before := snapshot()
	unchanged := func() {
		t.Helper()
		for table, value := range snapshot() {
			if value != before[table] {
				t.Fatal("migration modified", table)
			}
		}
		body, _, err := s.Get(ctx, key, "", 4096)
		if err != nil || string(body) != "# Preserved S3" {
			t.Fatal("S3 changed", string(body), err)
		}
	}
	count := func(want int) {
		t.Helper()
		var got int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM object_write_versions`).Scan(&got); err != nil || got != want {
			t.Fatal(got, err)
		}
	}
	if _, err = provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	count(0)
	unchanged()
	var typ string
	if err = pool.QueryRow(ctx, `SELECT atttypid::regtype::text FROM pg_attribute WHERE attrelid='object_write_versions'::regclass AND attname='scope'`).Scan(&typ); err != nil || typ != "uuid" {
		t.Fatal(typ, err)
	}
	if err = pool.QueryRow(ctx, `SELECT confrelid::regclass::text FROM pg_constraint WHERE conrelid='object_write_versions'::regclass AND conname='object_write_versions_scope_owner_fkey'`).Scan(&target); err != nil || target != "storage_connections" {
		t.Fatal(target, err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO object_write_versions(scope,owner,key,revision,present) VALUES($1,$2,'test/key',$3,true)`, n.ConnectionID, testutil.Other, revision); err == nil {
		t.Fatal("foreign connection owner accepted")
	}
	exec(`INSERT INTO object_write_versions(scope,owner,key,revision,present) VALUES($1,$2,'test/key',$3,true)`, n.ConnectionID, n.Owner, revision)
	if _, err = provider.DownTo(ctx, 5); err != nil {
		t.Fatal(err)
	}
	count(0)
	unchanged()
	if err = pool.QueryRow(ctx, `SELECT atttypid::regtype::text FROM pg_attribute WHERE attrelid='object_write_versions'::regclass AND attname='scope'`).Scan(&typ); err != nil || typ != "text" {
		t.Fatal(typ, err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO object_write_versions(scope,owner,key,revision,present) VALUES($1,$2,'test/key',$3,true)`, n.ConnectionID, n.Owner, revision); err == nil {
		t.Fatal("downgrade did not restore notebook FK")
	}
	exec(`INSERT INTO object_write_versions(scope,owner,key,revision,present) VALUES($1,$2,$3,$4,true)`, n.ID, n.Owner, key, revision)
	if _, err = provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	count(0)
	exec(`INSERT INTO object_write_versions(scope,owner,key,revision,present) VALUES($1,$2,'test/key',$3,true)`, n.ConnectionID, n.Owner, revision)
	if err = registry.Repo.RemoveNotebook(ctx, n.Owner, n.ID, n.Revision); err != nil {
		t.Fatal(err)
	}
	count(1)
	c, err := registry.Repo.Connection(ctx, n.Owner, n.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if err = registry.Repo.RemoveConnection(ctx, c.Owner, c.ID, c.Revision); err != nil {
		t.Fatal(err)
	}
	count(0)
}
