package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/db"
	"github.com/razzie-cloud/notes/internal/index"
	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/internal/web"
)

type App struct {
	Config    config.Config
	Engine    *index.Engine
	templates *template.Template
	ready     atomic.Int64
	Repo      *store.Repository
	Notebook  store.Notebook
	Notebooks []store.Notebook
	Writable  bool
}

func New(c config.Config, e *index.Engine) (*App, error) {
	t, err := template.New("page").Funcs(template.FuncMap{"query": url.QueryEscape, "base": path.Base, "attachmentURL": func(book, key string) string {
		return (&url.URL{Path: "/v1/attachments/" + key, RawQuery: "notebook=" + url.QueryEscape(book)}).String()
	}, "noteURL": func(book, key string) string {
		return (&url.URL{Path: "/v1/notes/" + key, RawQuery: "notebook=" + url.QueryEscape(book)}).String()
	}}).Parse(web.Templates)
	if err != nil {
		return nil, err
	}
	a := &App{Config: c, Engine: e, templates: t}
	if e != nil {
		a.Repo = e.Registry.Repo
	}
	return a, nil
}
func (a *App) Health(ctx context.Context) {
	var previous *bool
	for {
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := a.Engine.Pool.Ping(probe)
		cancel()
		healthy := err == nil
		if previous == nil && !healthy || previous != nil && *previous != healthy {
			if healthy {
				slog.Info("Dependency recovered", "event", "health.recovered", "dependency", "postgres")
			} else {
				slog.Warn("Dependency unavailable", "event", "health.failed", "dependency", "postgres")
			}
		}
		previous = &healthy
		if err == nil {
			a.ready.Store(time.Now().Unix())
		} else {
			a.ready.Store(0)
		}
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /storage", a.storagePage)
	mux.HandleFunc("POST /connections", a.saveConnection)
	mux.HandleFunc("POST /connections/{id}", a.saveConnection)
	mux.HandleFunc("POST /connections/{id}/delete", a.removeConnection)
	mux.HandleFunc("POST /notebooks", a.saveNotebook)
	mux.HandleFunc("POST /notebooks/{id}", a.saveNotebook)
	mux.HandleFunc("POST /notebooks/{id}/delete", a.removeNotebook)
	mux.HandleFunc("GET /recovery/{id}", a.recovery)

	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(web.Static)))
	mux.HandleFunc("GET /v1/notes", a.notebook((*App).list))
	mux.HandleFunc("GET /v1/notes/{key...}", a.notebook((*App).read))
	mux.HandleFunc("PUT /v1/notes/{key...}", a.notebook((*App).write))
	mux.HandleFunc("DELETE /v1/notes/{key...}", a.notebook((*App).remove))
	mux.HandleFunc("GET /v1/attachments", a.notebook((*App).attachments))
	mux.HandleFunc("GET /v1/attachments/{key...}", a.notebook((*App).read))
	mux.HandleFunc("PUT /v1/attachments/{key...}", a.notebook((*App).write))
	mux.HandleFunc("DELETE /v1/attachments/{key...}", a.notebook((*App).remove))
	mux.HandleFunc("GET /v1/admin/index", a.notebook((*App).status))
	mux.HandleFunc("POST /v1/admin/rebuild", a.notebook((*App).rebuild))
	mux.HandleFunc("GET /{$}", a.notebook((*App).home))
	mux.HandleFunc("GET /edit", a.notebook((*App).edit))
	mux.HandleFunc("GET /note", a.notebook((*App).readPage))
	mux.HandleFunc("GET /tags", a.notebook((*App).tagsPage))
	mux.HandleFunc("POST /preview", a.notebook((*App).preview))
	mux.HandleFunc("POST /attachments/delete", a.notebook((*App).deleteAttachmentForm))
	mux.HandleFunc("POST /save", a.notebook((*App).save))
	mux.HandleFunc("POST /delete", a.notebook((*App).deleteForm))
	mux.HandleFunc("POST /upload", a.notebook((*App).upload))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Ordinary HTML form navigations need their same-origin Origin header.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; script-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			if r.Method != "GET" && r.Method != "HEAD" {
				w.WriteHeader(405)
				return
			}
			if r.URL.Path == "/readyz" && (a.ready.Load() == 0 || time.Now().Unix()-a.ready.Load() > 30) {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(200)
			return
		}
		values := r.Header.Values("X-Auth-Request-User")
		if len(values) != 1 || !storage.Owner(values[0]) {
			http.Error(w, "Verified user required", 401)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != a.Config.Origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" || origin == "" && r.Header.Get("Cookie") != "" {
				http.Error(w, "Same-origin request required", 403)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/v1/admin/") && r.Header.Get("X-Notes-Admin") != "true" {
			http.Error(w, "Administrator required", 403)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
func owner(r *http.Request) string { return r.Header.Get("X-Auth-Request-User") }
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func (a *App) fail(w http.ResponseWriter, r *http.Request, err error) {
	status := 503
	msg := "Dependency unavailable; retry later"
	switch {
	case errors.Is(err, store.ErrReadOnly):
		status = 403
		msg = "Storage is read-only"
	case errors.Is(err, store.ErrConflict):
		status = 409
		msg = "Storage settings changed, name exists, or prefixes overlap"
	case errors.Is(err, storage.ErrCrypto):
		status = 422
		msg = "Cannot decrypt storage; check the crypt password and options"
	case errors.Is(err, store.ErrNotFound):
		status = 404
		msg = "Storage settings not found"
	case errors.Is(err, storage.ErrNotFound):
		status = 404
		msg = "Object not found"
	case errors.Is(err, storage.ErrConflict):
		status = 412
		msg = "Object changed; reload and compare before saving"
	case errors.Is(err, storage.ErrPath):
		status = 400
		msg = "Invalid object path"
	case errors.Is(err, storage.ErrTooLarge):
		status = 413
		msg = "Object exceeds size limit"
	}
	if status == 503 {
		a.logFailure(w, r, err)
	}
	http.Error(w, msg, status)
}
func (a *App) logFailure(w http.ResponseWriter, r *http.Request, err error) {
	id := uuid.NewString()
	w.Header().Set("X-Request-ID", id)
	attrs := []any{"event", "notes.request_failed", "request_id", id, "route", r.Pattern}
	slog.Warn("Request failed", append(attrs, storage.ErrorAttrs(err)...)...)
}
func (a *App) objectKey(r *http.Request) (key, kind string, err error) {
	relative := r.PathValue("key")
	kind = "notes"
	if strings.HasPrefix(r.URL.Path, "/v1/attachments/") {
		kind = "attachments"
	}
	key = a.Engine.Prefix + owner(r) + "/" + kind + "/" + relative
	if _, _, _, _, ok := storage.Split(a.Engine.Prefix, key); !ok {
		return "", "", storage.ErrPath
	}
	return
}
func (a *App) read(w http.ResponseWriter, r *http.Request) {
	key, kind, e := a.objectKey(r)
	if e != nil {
		a.fail(w, r, e)
		return
	}
	limit := a.Config.MaxNoteBytes
	if kind == "attachments" {
		limit = a.Config.MaxAttachmentBytes
	}
	b, m, e := a.Engine.Storage.Get(r.Context(), key, "", limit)
	if e != nil {
		a.fail(w, r, e)
		return
	}
	w.Header().Set("ETag", m.Validator())
	w.Header().Set("X-S3-Version-ID", m.Version)
	if kind == "notes" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(key)}))
	}
	w.Write(b)
}
func validator(r *http.Request) (string, bool) {
	match, none := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
	if match == "" && none == "*" {
		return "*", true
	}
	if none == "" && validETag(match) {
		return match, true
	}
	return "", false
}
func validETag(s string) bool {
	if len(s) < 2 || len(s) > 1024 || s[0] != '"' || s[len(s)-1] != '"' {
		return false
	}
	for _, b := range []byte(s[1 : len(s)-1]) {
		if b < 0x21 || b == 0x22 || b == 0x7f {
			return false
		}
	}
	return true
}
func (a *App) conflict(w http.ResponseWriter, r *http.Request, key string) {
	if m, e := a.Engine.Storage.Head(r.Context(), key); e == nil {
		w.Header().Set("ETag", m.Validator())
	}
	a.fail(w, r, storage.ErrConflict)
}
func (a *App) write(w http.ResponseWriter, r *http.Request) {
	key, kind, e := a.objectKey(r)
	if e != nil {
		a.fail(w, r, e)
		return
	}
	etag, ok := validator(r)
	if !ok {
		http.Error(w, "Use If-Match with the current ETag or If-None-Match: *", 428)
		return
	}
	limit := a.Config.MaxNoteBytes
	ct := "text/markdown; charset=utf-8"
	if kind == "attachments" {
		limit = a.Config.MaxAttachmentBytes
		ct = "application/octet-stream"
	}
	b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if e != nil {
		a.fail(w, r, storage.ErrTooLarge)
		return
	}
	if kind == "notes" {
		if _, e = index.Parse(key, b); e != nil {
			http.Error(w, e.Error(), 422)
			return
		}
	}
	m, e := a.put(r.Context(), key, b, etag, ct)
	if errors.Is(e, storage.ErrConflict) {
		a.conflict(w, r, key)
		return
	}
	if e != nil {
		a.fail(w, r, e)
		return
	}
	w.Header().Set("ETag", m.Validator())
	status := 200
	if etag == "*" {
		status = 201
	}
	jsonResponse(w, status, map[string]any{"key": r.PathValue("key"), "etag": m.Validator(), "index": "pending"})
}
func (a *App) put(ctx context.Context, key string, b []byte, etag, ct string) (storage.Meta, error) {
	tx, e := db.ObjectLock(ctx, a.Engine.Pool, a.Engine.Scope, key)
	if e != nil {
		return storage.Meta{}, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	ctx = store.WithTransaction(ctx, tx)
	m, e := a.Engine.Storage.Put(ctx, key, b, etag, ct)
	if e != nil {
		return m, e
	}
	// S3 success is authoritative. Queue/transaction failures cannot undo it.
	a.queueAfterWrite(ctx, tx, key)
	return m, nil
}
func (a *App) queueAfterWrite(ctx context.Context, tx pgx.Tx, key string) {
	queueTx, err := tx.Begin(ctx)
	if err == nil {
		_, err = a.Engine.Client.InsertTx(ctx, queueTx, index.ObjectArgs{Scope: a.Engine.Scope, Key: key}, nil)
		if err == nil {
			err = queueTx.Commit(ctx)
		} else {
			queueTx.Rollback(context.WithoutCancel(ctx))
		}
	}
	commitErr := tx.Commit(ctx)
	if err != nil || commitErr != nil {
		slog.Warn("S3 write saved; reconciliation will repair index", "event", "notes.index_enqueue_failed", "error_kind", "dependency")
	}
}
func (a *App) remove(w http.ResponseWriter, r *http.Request) {
	key, _, e := a.objectKey(r)
	if e != nil {
		a.fail(w, r, e)
		return
	}
	etag, ok := validator(r)
	if !ok || etag == "*" {
		http.Error(w, "Current If-Match ETag required", 428)
		return
	}
	if e = a.delete(r.Context(), key, etag); errors.Is(e, storage.ErrConflict) {
		a.conflict(w, r, key)
		return
	}
	if e != nil {
		a.fail(w, r, e)
		return
	}
	w.WriteHeader(204)
}
func (a *App) delete(ctx context.Context, key, etag string) error {
	tx, e := db.ObjectLock(ctx, a.Engine.Pool, a.Engine.Scope, key)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	ctx = store.WithTransaction(ctx, tx)
	if e = a.Engine.Storage.Delete(ctx, key, etag); e != nil {
		return e
	}
	a.queueAfterWrite(ctx, tx, key)
	return nil
}

type Note struct {
	Key             string    `json:"key"`
	Excerpt         string    `json:"-"`
	AttachmentCount int       `json:"-"`
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	Tags            []string  `json:"tags"`
	ETag            string    `json:"etag"`
	Modified        time.Time `json:"modified"`
	Error           string    `json:"index_error,omitempty"`
}

func page(r *http.Request) (int, int, error) {
	limit, offset := 50, 0
	var err error
	if s := r.URL.Query().Get("limit"); s != "" {
		limit, err = strconv.Atoi(s)
		if err != nil {
			return 0, 0, err
		}
	}
	if s := r.URL.Query().Get("offset"); s != "" {
		offset, err = strconv.Atoi(s)
		if err != nil {
			return 0, 0, err
		}
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 1000000 {
		return 0, 0, errors.New("invalid pagination")
	}
	return limit, offset, nil
}
func (a *App) notes(r *http.Request) ([]Note, error) {
	limit, offset, err := page(r)
	if err != nil {
		return nil, storage.ErrPath
	}
	q, tag := r.URL.Query().Get("q"), r.URL.Query().Get("tag")
	if len(q) > 1024 || len(tag) > 128 {
		return nil, storage.ErrPath
	}
	rows, err := a.Engine.Pool.Query(r.Context(), `SELECT relative_key,stable_id,title,tags,etag,modified,index_error,left(body,240), (SELECT count(*) FROM objects attachment WHERE attachment.scope=objects.scope AND attachment.owner=objects.owner AND attachment.kind='attachments' AND attachment.note_key=objects.relative_key) FROM objects WHERE scope=$1 AND owner=$2 AND kind='notes' AND ($3='' OR search @@ websearch_to_tsquery('simple',$3)) AND ($4='' OR tags @> ARRAY[$4]::text[]) ORDER BY CASE WHEN $3='' THEN 0 ELSE ts_rank(search,websearch_to_tsquery('simple',$3)) END DESC,modified DESC,key LIMIT $5 OFFSET $6`, a.Engine.Scope, owner(r), q, tag, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notes := []Note{}
	for rows.Next() {
		var n Note
		if err = rows.Scan(&n.Key, &n.ID, &n.Title, &n.Tags, &n.ETag, &n.Modified, &n.Error, &n.Excerpt, &n.AttachmentCount); err != nil {
			return nil, err
		}
		n.Excerpt = markdownExcerpt(n.Excerpt)
		notes = append(notes, n)
	}
	return notes, rows.Err()
}
func (a *App) list(w http.ResponseWriter, r *http.Request) {
	n, e := a.notes(r)
	if e != nil {
		a.fail(w, r, e)
		return
	}
	jsonResponse(w, 200, n)
}
func (a *App) attachments(w http.ResponseWriter, r *http.Request) {
	note := r.URL.Query().Get("note")
	if !storage.Path(note) || !strings.HasSuffix(note, ".md") {
		a.fail(w, r, storage.ErrPath)
		return
	}
	result, e := a.attachmentList(r, note)
	if e != nil {
		a.fail(w, r, e)
		return
	}
	jsonResponse(w, 200, result)
}

func (a *App) status(w http.ResponseWriter, r *http.Request) {
	var id, status, cursor, lastError string
	var listed, checked int64
	var started time.Time
	var completed *time.Time
	err := a.Engine.Pool.QueryRow(r.Context(), `SELECT id::text,status,cursor,listed,checked,started_at,completed_at,last_error FROM index_scans WHERE scope=$1`, a.Engine.Scope).Scan(&id, &status, &cursor, &listed, &checked, &started, &completed, &lastError)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonResponse(w, 200, map[string]string{"status": "pending"})
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var pending, discarded, indexErrors int64
	err = a.Engine.Pool.QueryRow(r.Context(), `SELECT count(*) FILTER(WHERE state IN ('available','pending','running','retryable','scheduled')),count(*) FILTER(WHERE state='discarded') FROM river_job WHERE args->>'scope'=$1`, a.Engine.Scope).Scan(&pending, &discarded)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	err = a.Engine.Pool.QueryRow(r.Context(), `SELECT count(*) FROM objects WHERE scope=$1 AND index_error<>''`, a.Engine.Scope).Scan(&indexErrors)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	jsonResponse(w, 200, map[string]any{"scan_id": id, "status": status, "listing_has_more": cursor != "", "listed": listed, "deletion_checks": checked, "started_at": started, "completed_at": completed, "last_error": lastError, "jobs_pending": pending, "jobs_discarded": discarded, "invalid_objects": indexErrors})
}
func (a *App) rebuild(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("restart") == "true" {
		if e := a.Engine.RestartScan(r.Context()); e != nil {
			a.fail(w, r, e)
			return
		}
	}
	if e := a.Engine.Rebuild(r.Context()); e != nil {
		a.fail(w, r, e)
		return
	}
	jsonResponse(w, 202, map[string]string{"status": "queued"})
}

type View struct {
	LandingOrigin                                 string
	Notes                                         []Note
	Tags                                          []TagCount
	NoteTags                                      []string
	Total                                         int
	TagsPage, Reading, Metadata, DraftFailed      bool
	Title, TagsText, Body, FrontMatter            string
	Rendered                                      template.HTML
	Modified                                      time.Time
	Attachments                                   []Attachment
	Notebook                                      store.Notebook
	Notebooks                                     []store.Notebook
	Writable                                      bool
	StorageNeeded                                 bool
	StoragePage                                   bool
	Connections                                   []store.Connection
	EditConnection                                *store.Connection
	EditNotebook                                  *store.Notebook
	Recoveries                                    []store.RecoveryInfo
	Query, Tag, Key, Content, ETag, Message, User string
	Editing                                       bool
	HasNext                                       bool
	Offset, Previous, Next                        int
}

func (a *App) render(w http.ResponseWriter, r *http.Request, v View, status int) {
	v.LandingOrigin = a.Config.LandingOrigin
	if v.LandingOrigin == "" {
		v.LandingOrigin = "https://razzie.cloud"
	}
	v.User = r.Header.Get("X-Auth-Request-Name")
	v.Notebook = a.Notebook
	v.Notebooks = a.Notebooks
	v.Writable = a.Writable
	if v.User == "" {
		v.User = "Your account"
	}
	if v.Editing && !v.Metadata {
		editorFields(&v)
	}
	v.DraftFailed = v.Editing && status >= 400
	if v.Editing {
		v.Rendered = renderMarkdown(v.Body, a.Notebook.ID, v.Key)
	}
	if a.Notebook.ID != "" {
		var err error
		v.Total, v.Tags, err = a.librarySummary(r)
		if err != nil && !v.Editing {
			a.fail(w, r, err)
			return
		}
		if v.Editing && v.ETag != "*" {
			v.Attachments, err = a.attachmentList(r, v.Key)
			// Supplemental attachment/index failures must not discard a draft or
			// turn a completed S3 save into a failed response.
			if err == nil && v.Writable {
				attachmentContext, cancel := context.WithTimeout(r.Context(), 2*time.Second)
				defer cancel()
				// Show a just-uploaded object immediately, even before its index job
				// runs. The query parameter identifies only a scoped HEAD request.
				if name := r.URL.Query().Get("uploaded"); storage.Path(name) && !strings.Contains(name, "/") {
					relative := v.Key + "/" + name
					found := false
					for _, item := range v.Attachments {
						found = found || item.Key == relative
					}
					if !found {
						meta, headErr := a.Engine.Storage.Head(attachmentContext, a.Engine.Prefix+owner(r)+"/attachments/"+relative)
						if headErr == nil {
							v.Attachments = append(v.Attachments, Attachment{Key: relative, Size: meta.Size, ETag: meta.ETag, WriteETag: meta.Validator()})
						}
					}
				}
				for i := range v.Attachments {
					if attachmentContext.Err() != nil {
						break
					}
					item := &v.Attachments[i]
					if item.WriteETag != "" {
						continue
					}
					meta, headErr := a.Engine.Storage.Head(attachmentContext, a.Engine.Prefix+owner(r)+"/attachments/"+item.Key)
					if headErr == nil {
						item.WriteETag = meta.Validator()
					}
				}
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if e := a.templates.ExecuteTemplate(w, "page", v); e != nil {
		slog.Warn("Template failed", "event", "notes.render_failed")
	}
}
func (a *App) home(w http.ResponseWriter, r *http.Request) {
	n, e := a.notes(r)
	if e != nil {
		a.fail(w, r, e)
		return
	}
	limit, offset, _ := page(r)
	v := View{Notes: n, Query: r.URL.Query().Get("q"), Tag: r.URL.Query().Get("tag"), Offset: offset, Previous: max(0, offset-limit), Next: offset + limit, HasNext: len(n) == limit}
	a.render(w, r, v, 200)
}
func (a *App) edit(w http.ResponseWriter, r *http.Request) {
	v := View{Editing: true, Key: r.URL.Query().Get("key"), ETag: "*"}
	if v.Key != "" {
		key := a.Engine.Prefix + owner(r) + "/notes/" + v.Key
		if _, _, _, _, ok := storage.Split(a.Engine.Prefix, key); !ok {
			a.fail(w, r, storage.ErrPath)
			return
		}
		b, m, e := a.Engine.Storage.Get(r.Context(), key, "", a.Config.MaxNoteBytes)
		if e != nil {
			a.fail(w, r, e)
			return
		}
		v.Content = string(b)
		v.ETag = m.Validator()
	} else {
		v.Key = uuid.NewString() + ".md"
		v.Content = ""
	}
	a.render(w, r, v, 200)
}
func (a *App) save(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.Config.MaxNoteBytes*4+4096)
	if e := r.ParseForm(); e != nil {
		a.fail(w, r, storage.ErrTooLarge)
		return
	}
	v := View{Editing: true, Key: r.PostForm.Get("key"), Content: r.PostForm.Get("content"), ETag: r.PostForm.Get("etag")}
	if r.PostForm.Get("metadata") == "on" {
		v.Metadata = true
		v.Title, v.TagsText, v.Body, v.FrontMatter = r.PostForm.Get("title"), r.PostForm.Get("tags"), v.Content, r.PostForm.Get("front_matter")
		content, err := composeMarkdown(v.FrontMatter, v.Body, v.Title, v.TagsText)
		if err != nil {
			v.Message = "Check the note metadata. Your draft is preserved below."
			a.render(w, r, v, 422)
			return
		}
		v.Content = content
	}
	key := a.Engine.Prefix + owner(r) + "/notes/" + v.Key
	if _, _, _, _, ok := storage.Split(a.Engine.Prefix, key); !ok {
		a.fail(w, r, storage.ErrPath)
		return
	}
	if int64(len(v.Content)) > a.Config.MaxNoteBytes {
		a.fail(w, r, storage.ErrTooLarge)
		return
	}
	if _, err := index.Parse(key, []byte(v.Content)); err != nil {
		v.Message = err.Error()
		a.render(w, r, v, 422)
		return
	}
	if v.ETag != "*" && !validETag(v.ETag) {
		http.Error(w, "Current ETag required", 428)
		return
	}
	m, e := a.put(r.Context(), key, []byte(v.Content), v.ETag, "text/markdown; charset=utf-8")
	status := 200
	if e != nil {
		status = 503
		v.Message = "Save failed. Your draft is preserved below; retry when storage is available."
		var failure *storage.Failure
		if errors.As(e, &failure) && failure.Code == "NotImplemented" && failure.Feature == "conditional_write" {
			v.Message = "This storage provider does not support conditional writes. Your draft is preserved below. Review Conditional writes in the connection settings."
		}
		if errors.Is(e, store.ErrReadOnly) {
			status = 403
			v.Message = "Storage is now read-only. Your draft is preserved below."
			a.Writable = false
		}
		if errors.Is(e, storage.ErrConflict) {
			status = 412
			v.Message = "This note changed elsewhere. Your draft is preserved below. Open the current note in another tab and compare before saving."
		}
	} else {
		v.ETag = m.Validator()
		v.Message = "Saved. Search and attachments update in the background."
	}
	if status == 503 {
		a.logFailure(w, r, e)
	}
	a.render(w, r, v, status)
}
func (a *App) deleteForm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	key := a.Engine.Prefix + owner(r) + "/notes/" + r.PostForm.Get("key")
	if _, _, _, _, ok := storage.Split(a.Engine.Prefix, key); !ok {
		a.fail(w, r, storage.ErrPath)
		return
	}
	etag := r.PostForm.Get("etag")
	if !validETag(etag) {
		http.Error(w, "Current ETag required", 428)
		return
	}
	if e := a.delete(r.Context(), key, etag); e != nil {
		a.fail(w, r, e)
		return
	}
	http.Redirect(w, r, "/?notebook="+url.QueryEscape(a.Notebook.ID), 303)
}
func (a *App) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, a.Config.MaxAttachmentBytes+8192)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		a.fail(w, r, storage.ErrTooLarge)
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, header, e := r.FormFile("file")
	if e != nil {
		http.Error(w, "Select an attachment", 400)
		return
	}
	defer file.Close()
	name := header.Filename
	if !storage.Path(name) || strings.Contains(name, "/") {
		a.fail(w, r, storage.ErrPath)
		return
	}
	note := r.PostForm.Get("key")
	key := a.Engine.Prefix + owner(r) + "/attachments/" + note + "/" + name
	if _, _, _, _, ok := storage.Split(a.Engine.Prefix, key); !ok {
		a.fail(w, r, storage.ErrPath)
		return
	}
	b, e := io.ReadAll(io.LimitReader(file, a.Config.MaxAttachmentBytes+1))
	if e != nil || int64(len(b)) > a.Config.MaxAttachmentBytes {
		a.fail(w, r, storage.ErrTooLarge)
		return
	}
	if _, e = a.put(r.Context(), key, b, "*", "application/octet-stream"); e != nil {
		a.fail(w, r, e)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/edit?key=%s&notebook=%s&uploaded=%s", url.QueryEscape(note), url.QueryEscape(a.Notebook.ID), url.QueryEscape(name)), 303)
}
