package app

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/testutil"
	"testing"
	"time"
)

func TestSharedPrefixChangeWaitsForMapping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const creator = "11111111-1111-4111-8111-111111111111"
	const member = "22222222-2222-4222-8222-222222222222"
	repo := &store.Repository{Pool: testutil.Database(t)}
	c := store.Connection{Owner: creator, Name: "Concurrent", Provider: "AWS", Region: "us-east-1", ConditionalWrites: true, AccessKey: "test", SecretKey: "test", Shared: true, UserIDPrefix: true}
	admin := store.WithAdmin(ctx, true)
	if err := repo.SaveConnection(admin, &c); err != nil {
		t.Fatal(err)
	}
	tx, err := repo.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT id FROM storage_connections WHERE id=$1 FOR SHARE`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO notebooks(id,owner,connection_id,name,bucket,prefix) VALUES($1,$2,$3,'Pending','bucket','projects')`, uuid.NewString(), member, c.ID); err != nil {
		t.Fatal(err)
	}
	c.UserIDPrefix = false
	result := make(chan error, 1)
	go func() { result <- repo.SaveConnection(admin, &c) }()
	select {
	case err := <-result:
		t.Fatal("prefix change did not wait for mapping", err)
	case <-time.After(40 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, store.ErrConflict) {
			t.Fatal("prefix redirected after concurrent mapping commit", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Direct repo.Pool.Exec(ctx, cannot bypass either immutable sharing or the attached-prefix guard.
	for _, query := range []string{`UPDATE storage_connections SET shared=false WHERE id=$1`, `UPDATE storage_connections SET user_id_prefix=false WHERE id=$1`} {
		if _, err := repo.Pool.Exec(ctx, query, c.ID); err == nil {
			t.Fatal("database policy bypass", query)
		}
	}
}
