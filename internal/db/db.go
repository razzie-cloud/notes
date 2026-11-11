package db

import (
	"context"
	"database/sql"
	"embed"
	"io"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Migrate(ctx context.Context, database *sql.DB, pool *pgxpool.Pool) error {
	root, e := fs.Sub(migrations, "migrations")
	if e != nil {
		return e
	}
	locker, e := lock.NewPostgresSessionLocker()
	if e != nil {
		return e
	}
	provider, e := goose.NewProvider(goose.DialectPostgres, database, root, goose.WithSessionLocker(locker), goose.WithLogger(goose.NopLogger()))
	if e != nil {
		return e
	}
	if _, e = provider.Up(ctx); e != nil {
		return e
	}
	migrator, e := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if e != nil {
		return e
	}
	// River migrations need separate commits (enum values added by one version
	// are used by later versions). Serialize the entire sequence with a session
	// lock while River retains its own per-version transaction boundaries.
	conn, e := pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	if _, e = conn.Exec(ctx, `SELECT pg_advisory_lock(706648431)`); e != nil {
		return e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanup, `SELECT pg_advisory_unlock(706648431)`); err != nil {
			conn.Conn().Close(cleanup)
		}
	}()
	_, e = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return e
}

// ObjectLock serializes indexing and service writes across replicas. ETags are
// opaque and never ordered. The transaction must remain open until S3 work ends.
func ObjectLock(ctx context.Context, pool *pgxpool.Pool, scope, key string) (pgx.Tx, error) {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	if e = LockObject(ctx, tx, scope, key); e != nil {
		tx.Rollback(ctx)
		return nil, e
	}
	return tx, nil
}

func LockObject(ctx context.Context, tx pgx.Tx, scope, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, scope+":"+key)
	return err
}
