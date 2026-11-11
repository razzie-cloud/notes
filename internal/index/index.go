// Package index maintains disposable PostgreSQL rows from authoritative S3.
package index

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/db"
	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

type ScanArgs struct {
	Scope string `json:"scope"`
}

func (ScanArgs) Kind() string { return "scan_bucket" }
func (ScanArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "maintenance", MaxAttempts: 8, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}}}
}

type ObjectArgs struct {
	Scope string `json:"scope"`
	Key   string `json:"key"`
}

func (ObjectArgs) Kind() string { return "index_object" }
func (ObjectArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "indexing", MaxAttempts: 8}
}

type Engine struct {
	Pool          *pgxpool.Pool
	Registry      *storage.Registry
	Storage       storage.Store
	Owner         string
	Client        *river.Client[pgx.Tx]
	Scope, Prefix string
	MaxNoteBytes  int64
}

func New(pool *pgxpool.Pool, registry *storage.Registry, c config.Config) (*Engine, error) {
	e := &Engine{Pool: pool, Registry: registry, MaxNoteBytes: c.MaxNoteBytes}
	workers := river.NewWorkers()
	river.AddWorker(workers, &scanWorker{E: e})
	river.AddWorker(workers, &objectWorker{E: e})
	river.AddWorker(workers, &scheduleWorker{E: e})
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Workers: workers, Queues: map[string]river.QueueConfig{"indexing": {MaxWorkers: c.Workers}, "maintenance": {MaxWorkers: 1}},
		JobTimeout: 2 * time.Minute,
		// River errors can contain SQL arguments. Workers emit classified events;
		// keep upstream raw errors and job arguments out of platform logs.
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PeriodicJobs: []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(c.ScanInterval), func() (river.JobArgs, *river.InsertOpts) { return ScheduleArgs{}, nil }, &river.PeriodicJobOpts{RunOnStart: true})},
	})
	if err != nil {
		return nil, err
	}
	e.Client = client
	return e, nil
}

// Bind checks notebook ownership before producing a storage/index view. The
// managed store rechecks current policies and credentials for every S3 operation.
func (e *Engine) Bind(ctx context.Context, owner, id string) (*Engine, error) {
	objectStore, n, err := e.Registry.Open(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	return &Engine{Pool: e.Pool, Registry: e.Registry, Storage: objectStore, Client: e.Client, Owner: owner, Scope: n.ID, Prefix: "", MaxNoteBytes: e.MaxNoteBytes}, nil
}
func (e *Engine) target(ctx context.Context, id string) (*Engine, error) {
	n, err := e.Registry.Repo.Target(ctx, id)
	if err != nil {
		return nil, err
	}
	return e.Bind(ctx, n.Owner, n.ID)
}

type ScheduleArgs struct {
	Cursor string `json:"cursor"`
}

func (ScheduleArgs) Kind() string { return "schedule_notebooks" }
func (ScheduleArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: "maintenance", MaxAttempts: 8, UniqueOpts: ScanArgs{}.InsertOpts().UniqueOpts}
}

type scheduleWorker struct {
	river.WorkerDefaults[ScheduleArgs]
	E *Engine
}

func (w *scheduleWorker) Work(ctx context.Context, job *river.Job[ScheduleArgs]) error {
	books, err := w.E.Registry.Repo.Targets(ctx, job.Args.Cursor)
	if err != nil {
		return errors.New("storage scheduling failed")
	}
	tx, err := w.E.Pool.Begin(ctx)
	if err != nil {
		return errors.New("storage scheduling failed")
	}
	defer tx.Rollback(ctx)
	for _, n := range books {
		if _, err = w.E.Client.InsertTx(ctx, tx, ScanArgs{Scope: n.ID}, nil); err != nil {
			return errors.New("scan scheduling failed")
		}
	}
	if len(books) == 200 {
		if _, err = w.E.Client.InsertTx(ctx, tx, ScheduleArgs{Cursor: books[len(books)-1].ID}, nil); err != nil {
			return errors.New("scan scheduling failed")
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return errors.New("scan scheduling failed")
	}
	return nil
}

func (e *Engine) Enqueue(ctx context.Context, key string) error {
	_, err := e.Client.Insert(ctx, ObjectArgs{Scope: e.Scope, Key: key}, nil)
	return err
}
func (e *Engine) Rebuild(ctx context.Context) error {
	_, err := e.Client.Insert(ctx, ScanArgs{Scope: e.Scope}, nil)
	return err
}

// RestartScan abandons only derived scan progress, under the same lock as pages.
// Useful when a provider invalidates a saved continuation token.
func (e *Engine) RestartScan(ctx context.Context) error {
	tx, err := e.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ctx = store.WithTransaction(ctx, tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1))`, e.Scope); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM scan_seen WHERE scope=$1`, e.Scope); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM index_scans WHERE scope=$1`, e.Scope); err != nil {
		return err
	}
	if _, err = e.Client.InsertTx(ctx, tx, ScanArgs{Scope: e.Scope}, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (e *Engine) insert(ctx context.Context, tx pgx.Tx, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	batch := make([]river.InsertManyParams, 0, len(keys))
	for _, key := range keys {
		batch = append(batch, river.InsertManyParams{Args: ObjectArgs{Scope: e.Scope, Key: key}})
	}
	_, err := e.Client.InsertManyTx(ctx, tx, batch)
	return err
}

type scanWorker struct {
	river.WorkerDefaults[ScanArgs]
	E *Engine
}

func (w *scanWorker) Work(ctx context.Context, job *river.Job[ScanArgs]) error {
	bound, err := w.E.target(ctx, job.Args.Scope)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return errors.New("storage settings unavailable")
	}

	// Limit each attempt's work. A snoozed job resumes the durable cursor without
	// consuming a retry and without monopolizing the maintenance queue forever.
	for range 20 {
		done, err := bound.ScanPage(ctx)
		if err != nil {
			slog.Warn("Bucket scan failed", "event", "notes.scan_failed", "error_kind", "dependency")
			mark, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			bound.Pool.Exec(mark, `UPDATE index_scans SET last_error='scan dependency failed; retry pending' WHERE scope=$1`, bound.Scope)
			return errors.New("scan dependency failed")
		}
		if done {
			return nil
		}
	}
	return river.JobSnooze(time.Second)
}

type objectWorker struct {
	river.WorkerDefaults[ObjectArgs]
	E *Engine
}

func (w *objectWorker) Work(ctx context.Context, job *river.Job[ObjectArgs]) error {
	bound, err := w.E.target(ctx, job.Args.Scope)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return errors.New("storage settings unavailable")
	}

	if err := bound.Index(ctx, job.Args.Key); err != nil {
		slog.Warn("Object indexing failed", "event", "notes.index_failed", "error_kind", "dependency")
		return errors.New("index dependency failed")
	}
	return nil
}

// ScanPage atomically commits one bounded listing/sweep page and its jobs. The
// scope advisory lock and row state prevent overlap even for duplicate jobs.
func (e *Engine) ScanPage(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	tx, err := e.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	ctx = store.WithTransaction(ctx, tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1))`, e.Scope); err != nil {
		return false, err
	}
	var id, status, cursor, sweep string
	err = tx.QueryRow(ctx, `SELECT id::text,status,cursor,sweep_cursor FROM index_scans WHERE scope=$1 FOR UPDATE`, e.Scope).Scan(&id, &status, &cursor, &sweep)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && status == "complete" {
		id = uuid.NewString()
		status = "listing"
		cursor = ""
		sweep = ""
		_, err = tx.Exec(ctx, `INSERT INTO index_scans(scope,id,status) VALUES($1,$2,'listing') ON CONFLICT(scope) DO UPDATE SET id=$2,status='listing',cursor='',sweep_cursor='',started_at=now(),completed_at=NULL,listed=0,checked=0,last_error=''`, e.Scope, id)
	}
	if err != nil {
		return false, err
	}
	done := false
	if status == "listing" {
		page, err := e.Storage.List(ctx, e.Prefix, cursor)
		if err != nil {
			return false, err
		}
		keys := []string{}
		for _, object := range page.Objects {
			if _, _, _, _, ok := storage.Split(e.Prefix, object.Key); !ok {
				continue
			}
			keys = append(keys, object.Key)
			if _, err = tx.Exec(ctx, `INSERT INTO scan_seen(scope,scan_id,key) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, e.Scope, id, object.Key); err != nil {
				return false, err
			}
		}
		if err = e.insert(ctx, tx, keys); err != nil {
			return false, err
		}
		if page.Next == "" {
			status = "reconciling"
		}
		if _, err = tx.Exec(ctx, `UPDATE index_scans SET cursor=$2,status=$3,listed=listed+$4,last_error='' WHERE scope=$1`, e.Scope, page.Next, status, len(keys)); err != nil {
			return false, err
		}
	} else {
		// A successful listing is a prerequisite. HEAD/GET in Index remains the
		// authority for deletion, protecting keys created or changed during listing.
		rows, err := tx.Query(ctx, `SELECT o.key FROM objects o WHERE o.scope=$1 AND o.key>$3 AND NOT EXISTS(SELECT 1 FROM scan_seen s WHERE s.scope=o.scope AND s.scan_id=$2 AND s.key=o.key) ORDER BY o.key LIMIT 200`, e.Scope, id, sweep)
		if err != nil {
			return false, err
		}
		keys := []string{}
		for rows.Next() {
			var k string
			if err = rows.Scan(&k); err != nil {
				rows.Close()
				return false, err
			}
			keys = append(keys, k)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
		if err = e.insert(ctx, tx, keys); err != nil {
			return false, err
		}
		if len(keys) == 0 {
			done = true
			_, err = tx.Exec(ctx, `UPDATE index_scans SET status='complete',completed_at=now(),last_error='' WHERE scope=$1`, e.Scope)
			if err == nil {
				_, err = tx.Exec(ctx, `DELETE FROM scan_seen WHERE scope=$1`, e.Scope)
			}
		} else {
			_, err = tx.Exec(ctx, `UPDATE index_scans SET sweep_cursor=$2,checked=checked+$3,last_error='' WHERE scope=$1`, e.Scope, keys[len(keys)-1], len(keys))
		}
		if err != nil {
			return false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	if done {
		slog.Info("Bucket scan enqueued", "event", "notes.scan_complete")
	}
	return done, nil
}

// Index always fetches current metadata, never trusts an old job's ETag, and
// serializes per key. A conditional GET and final HEAD prevent LIST/GET races.
func (e *Engine) Index(ctx context.Context, key string) error {
	owner, kind, relative, note, ok := storage.Split(e.Prefix, key)
	if !ok || owner != e.Owner {
		return nil
	}
	tx, err := db.ObjectLock(ctx, e.Pool, e.Scope, key)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ctx = store.WithTransaction(ctx, tx)
	meta, err := e.Storage.Head(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		if _, err = tx.Exec(ctx, `DELETE FROM objects WHERE scope=$1 AND owner=$2 AND key=$3`, e.Scope, owner, key); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	var old storage.Meta
	err = tx.QueryRow(ctx, `SELECT etag,version_id,size,modified FROM objects WHERE scope=$1 AND owner=$2 AND key=$3`, e.Scope, owner, key).Scan(&old.ETag, &old.Version, &old.Size, &old.Modified)
	if err == nil && meta.Equal(old) {
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var d Document
	d.Tags = []string{}
	parseError := ""
	if kind == "notes" {
		if meta.Size > e.MaxNoteBytes {
			parseError = "note exceeds indexing limit"
			d.Title = relative
		} else {
			body, fetched, err := e.Storage.Get(ctx, key, meta.ETag, e.MaxNoteBytes)
			if err != nil {
				return err
			}
			// Some compatible providers omit version IDs on GET. The final
			// HEAD still verifies the exact discovered version before commit.
			if fetched.Version == "" {
				fetched.Version = meta.Version
			}
			if !fetched.Equal(meta) {
				return storage.ErrConflict
			}
			d, err = Parse(relative, body)
			if err != nil {
				parseError = "invalid Markdown metadata"
				d = Document{Title: relative, Tags: []string{}}
			}
		}
	}
	current, err := e.Storage.Head(ctx, key)
	if err != nil {
		return err
	}
	if !current.Equal(meta) {
		return storage.ErrConflict
	}
	var created any
	if !d.Created.IsZero() {
		created = d.Created
	}
	_, err = tx.Exec(ctx, `INSERT INTO objects(scope,owner,key,kind,relative_key,note_key,etag,version_id,size,modified,content_type,stable_id,title,body,tags,created,index_error)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
 ON CONFLICT(scope,key) DO UPDATE SET owner=EXCLUDED.owner,kind=EXCLUDED.kind,relative_key=EXCLUDED.relative_key,note_key=EXCLUDED.note_key,etag=EXCLUDED.etag,version_id=EXCLUDED.version_id,size=EXCLUDED.size,modified=EXCLUDED.modified,content_type=EXCLUDED.content_type,stable_id=EXCLUDED.stable_id,title=EXCLUDED.title,body=EXCLUDED.body,tags=EXCLUDED.tags,created=EXCLUDED.created,index_error=EXCLUDED.index_error,indexed_at=now()`, e.Scope, owner, key, kind, relative, note, meta.ETag, meta.Version, meta.Size, meta.Modified, meta.ContentType, d.ID, d.Title, d.Body, d.Tags, created, parseError)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
