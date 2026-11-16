package app

import (
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/razzie-cloud/notes/internal/storage"
	"github.com/razzie-cloud/notes/internal/store"
)

func (a *App) notebook(fn func(*App, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		books, e := a.Repo.Notebooks(r.Context(), owner(r))
		if e != nil {
			a.fail(w, r, e)
			return
		}
		id := r.URL.Query().Get("notebook")
		if id == "" {
			if len(books) == 0 {
				if r.Method == "GET" && r.URL.Path == "/" {
					connections, e := a.Repo.Connections(r.Context(), owner(r))
					if e != nil {
						a.fail(w, r, e)
						return
					}
					a.render(w, r, View{StorageNeeded: true, Connections: connections}, 200)
				} else {
					http.Error(w, "Add a notebook at /notebooks first", 409)
				}
				return
			}
			if len(books) > 1 && strings.HasPrefix(r.URL.Path, "/v1/") {
				http.Error(w, "Select a notebook with ?notebook=<id>", 400)
				return
			}
			id = books[0].ID
		}
		bound, e := a.Engine.Bind(r.Context(), owner(r), id)
		if e != nil {
			a.fail(w, r, e)
			return
		}
		n, e := a.Repo.Notebook(r.Context(), owner(r), id)
		if e != nil {
			a.fail(w, r, e)
			return
		}
		c, e := a.Repo.Connection(r.Context(), owner(r), n.ConnectionID)
		if e != nil {
			a.fail(w, r, e)
			return
		}
		child := &App{Config: a.Config, Engine: bound, Repo: a.Repo, templates: a.templates, Notebook: n, Notebooks: books, Writable: n.Writable(c)}
		if r.Method != "GET" && r.Method != "HEAD" && !child.Writable && !strings.HasPrefix(r.URL.Path, "/v1/admin/") {
			a.fail(w, r, store.ErrReadOnly)
			return
		}
		fn(child, w, r)
	}
}
func (a *App) storagePage(w http.ResponseWriter, r *http.Request) {
	// Preserve existing bookmarked notebook edit links with the new presentation.
	if r.URL.Query().Get("notebook") != "" {
		a.notebooksPage(w, r)
		return
	}
	connections, e := a.Repo.Connections(r.Context(), owner(r))
	if e != nil {
		a.fail(w, r, e)
		return
	}
	books, e := a.Repo.Notebooks(r.Context(), owner(r))
	if e != nil {
		a.fail(w, r, e)
		return
	}
	v := View{StoragePage: true, Connections: connections, EditConnection: &store.Connection{Provider: "AWS", Region: "us-east-1", ConditionalWrites: true}}
	v.ConnectionForm = r.URL.Query().Get("connection") != ""
	if id := r.URL.Query().Get("connection"); id != "" && id != "new" {
		c, e := a.Repo.Connection(r.Context(), owner(r), id)
		if e != nil {
			a.fail(w, r, e)
			return
		}
		if c.Shared && !store.IsAdmin(r.Context()) {
			a.fail(w, r, store.ErrForbidden)
			return
		}
		v.EditConnection = &c
		v.ConnectionLocked = c.InUse
		for _, book := range books {
			v.ConnectionLocked = v.ConnectionLocked || book.ConnectionID == c.ID
		}
	}

	a.renderStorage(w, r, v, books)
}
func (a *App) renderStorage(w http.ResponseWriter, r *http.Request, v View, books []store.Notebook) {
	v.Admin = store.IsAdmin(r.Context())
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "connection-dialog-content" {
		v.ConnectionDialog = true
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := a.templates.ExecuteTemplate(w, "connection-dialog", v); err != nil {
			slog.Warn("Template failed", "event", "notes.render_failed")
		}
		return
	}
	child := &App{Config: a.Config, Engine: a.Engine, Repo: a.Repo, templates: a.templates, Notebooks: books}
	child.render(w, r, v, 200)
}
func form(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if r.ParseForm() != nil {
		http.Error(w, "Invalid settings form", 400)
		return false
	}
	return true
}
func checked(r *http.Request, key string) bool { return r.PostForm.Get(key) == "on" }
func revision(r *http.Request) int64 {
	v, _ := strconv.ParseInt(r.PostForm.Get("revision"), 10, 64)
	return v
}
func (a *App) saveConnection(w http.ResponseWriter, r *http.Request) {
	if !form(w, r) {
		return
	}
	c := store.Connection{ID: r.PathValue("id"), Owner: owner(r), Name: strings.TrimSpace(r.PostForm.Get("name")), Provider: r.PostForm.Get("provider"), Endpoint: strings.TrimSpace(r.PostForm.Get("endpoint")), Region: strings.TrimSpace(r.PostForm.Get("region")), PathStyle: checked(r, "path_style"), ReadOnly: checked(r, "read_only"), ConditionalWrites: checked(r, "conditional_writes"), AccessKey: r.PostForm.Get("access_key"), SecretKey: r.PostForm.Get("secret_key"), SessionToken: r.PostForm.Get("session_token"), Revision: revision(r)}
	c.Bucket = strings.TrimSpace(r.PostForm.Get("bucket"))
	c.Shared = checked(r, "shared")
	c.UserIDPrefix = checked(r, "user_id_prefix")
	if c.ID != "" {
		old, err := a.Repo.Connection(r.Context(), owner(r), c.ID)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if old.Shared && !store.IsAdmin(r.Context()) {
			a.fail(w, r, store.ErrForbidden)
			return
		}
		if _, submitted := r.PostForm["shared"]; submitted && c.Shared != old.Shared {
			a.fail(w, r, store.ErrConflict)
			return
		}
		c.Shared, c.Owner = old.Shared, old.Owner
	}
	if c.Shared && !store.IsAdmin(r.Context()) {
		a.fail(w, r, store.ErrForbidden)
		return
	}
	if c.UserIDPrefix && !c.Shared {
		a.connectionValidation(w, r, "User ID prefixes require a shared connection")
		return
	}
	if c.Bucket != "" && (!bucketPattern.MatchString(c.Bucket) || strings.Contains(c.Bucket, "..")) {
		a.connectionValidation(w, r, "Enter a valid fixed S3 bucket name, or leave it blank")
		return
	}
	if !store.ValidName(c.Name) || len(c.Region) > 100 || c.Region == "" || (c.Provider != "AWS" && c.Provider != "Other" && c.Provider != "Backblaze") || len(c.AccessKey) > 4096 || len(c.SecretKey) > 8192 || len(c.SessionToken) > 16384 {
		a.connectionValidation(w, r, "Enter a name, provider, region and valid credentials")
		return
	}
	if c.Endpoint == "" && c.Provider != "AWS" {
		a.connectionValidation(w, r, "This provider requires an S3 endpoint")
		return
	}
	policy, e := storage.HTTPClient(c.Endpoint, a.Config.PrivateEndpoints, a.Config.AllowHTTP)
	if e != nil {
		a.connectionValidation(w, r, "Endpoint must use HTTPS; private/HTTP endpoints require operator configuration")
		return
	}
	policy.CloseIdleConnections()
	if c.ID == "" && (c.AccessKey == "" || c.SecretKey == "") {
		a.connectionValidation(w, r, "Access key and secret key are required")
		return
	}
	c.RetainSession = c.ID != "" && c.SessionToken == "" && !checked(r, "clear_session_token")
	if e = a.Repo.SaveConnection(r.Context(), &c); e != nil {
		a.fail(w, r, e)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Location", `{"path":"/storage","target":"#app-layout","select":"#app-layout","swap":"outerHTML"}`)
		return
	}
	http.Redirect(w, r, "/storage", 303)
}

// Keep connection drafts in the browser, including newly entered credentials.
// Error responses contain only a safe message, never submitted or stored secrets.
func (a *App) connectionError(w http.ResponseWriter, r *http.Request, message string, status int) bool {
	if r.Header.Get("HX-Request") != "true" {
		return false
	}
	target := r.Header.Get("HX-Target")
	if target != "connection-feedback" && target != "connection-dialog-content" {
		return false
	}
	if target == "connection-dialog-content" {
		target = "connection-list-feedback"
	}
	w.Header().Set("HX-Retarget", "#"+target)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := a.templates.ExecuteTemplate(w, "connection-error", View{Message: message}); err != nil {
		slog.Warn("Template failed", "event", "notes.render_failed")
	}
	return true
}

func (a *App) connectionValidation(w http.ResponseWriter, r *http.Request, message string) {
	if !a.connectionError(w, r, message, http.StatusUnprocessableEntity) {
		http.Error(w, message, http.StatusUnprocessableEntity)
	}
}

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

func (a *App) saveNotebook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if r.ParseForm() != nil {
		a.notebookValidation(w, r, "Invalid notebook form")
		return
	}
	if id := r.PathValue("id"); id != "" {
		n, e := a.Repo.Notebook(r.Context(), owner(r), id)
		if e != nil {
			a.fail(w, r, e)
			return
		}
		if n.Revision != revision(r) {
			a.fail(w, r, store.ErrConflict)
			return
		}
		n.Name = strings.TrimSpace(r.PostForm.Get("name"))
		n.ReadOnly = checked(r, "read_only")
		if !store.ValidName(n.Name) {
			a.notebookValidation(w, r, "Enter a notebook name")
			return
		}
		if e = a.Repo.SaveNotebook(r.Context(), &n, store.Secrets{}); e != nil {
			a.fail(w, r, e)
			return
		}
		notebookSaved(w, r)
		return
	}
	n := store.Notebook{Owner: owner(r), Name: strings.TrimSpace(r.PostForm.Get("name")), ConnectionID: r.PostForm.Get("connection_id"), Bucket: strings.TrimSpace(r.PostForm.Get("bucket")), Prefix: strings.TrimSuffix(strings.TrimSpace(r.PostForm.Get("prefix")), "/"), ReadOnly: checked(r, "read_only"), Encrypted: checked(r, "encrypted"), FilenameEncryption: r.PostForm.Get("filename_encryption"), FilenameEncoding: r.PostForm.Get("filename_encoding"), DirectoryEncryption: checked(r, "directory_encryption"), Suffix: r.PostForm.Get("suffix")}
	if !store.ValidName(n.Name) || (n.Prefix != "" && !storage.Path(n.Prefix)) {
		a.notebookValidation(w, r, "Enter a notebook name, bucket and safe relative prefix")
		return
	}
	c, e := a.Repo.Credentials(r.Context(), owner(r), n.ConnectionID)
	if e != nil {
		a.fail(w, r, e)
		return
	}
	n.Bucket, e = c.BucketFor(n.Bucket)
	if e != nil {
		a.notebookValidation(w, r, "Bucket is fixed by the connection")
		return
	}
	if !bucketPattern.MatchString(n.Bucket) || strings.Contains(n.Bucket, "..") {
		a.notebookValidation(w, r, "Enter a valid S3 bucket name")
		return
	}
	n.ConnectionRevision = c.Revision
	secret := store.Secrets{Password: r.PostForm.Get("crypt_password"), Password2: r.PostForm.Get("crypt_password2")}
	if n.Encrypted && (secret.Password == "" || len(secret.Password) > 4096 || len(secret.Password2) > 4096 || (n.FilenameEncryption != "standard" && n.FilenameEncryption != "off") || (n.FilenameEncoding != "base32" && n.FilenameEncoding != "base64" && n.FilenameEncoding != "base32768") || !strings.HasPrefix(n.Suffix, ".") || len(n.Suffix) > 20 || strings.ContainsAny(n.Suffix, "/\\\x00\r\n") || !storage.Path("file"+n.Suffix)) {
		a.notebookValidation(w, r, "Enter a crypt password and valid crypt options")
		return
	}
	if !n.Encrypted {
		n.FilenameEncryption = "standard"
		n.FilenameEncoding = "base32"
		n.Suffix = ".bin"
	}
	mapped, close, e := a.Engine.Registry.Direct(c, n, secret)
	if e != nil {
		a.fail(w, r, e)
		return
	}
	defer close()
	// Always check credentials/bucket accessibility. For a new crypt mapping,
	// inspect raw storage so a wrong password cannot hide pre-existing objects.
	p, e := mapped.Raw.List(r.Context(), mapped.Prefix, "")
	if e != nil {
		a.fail(w, r, e)
		return
	}
	if n.Encrypted {
		if checked(r, "new_crypt_prefix") {
			if len(p.Objects) != 0 || p.Next != "" {
				a.notebookValidation(w, r, "Prefix contains data. Verify an existing encrypted file instead")
				return
			}
		} else {
			verify := r.PostForm.Get("verify_path")
			if !storage.Path(verify) {
				a.notebookValidation(w, r, "Enter a known decrypted file path, or confirm a new empty prefix")
				return
			}
			b, _, e := mapped.Get(r.Context(), owner(r)+"/"+verify, "", a.Config.MaxAttachmentBytes)
			if e != nil || len(b) == 0 {
				a.notebookValidation(w, r, "Cannot verify crypt settings with that nonempty file")
				return
			}
		}
	}
	if e = a.Repo.SaveNotebook(r.Context(), &n, secret); e != nil {
		a.fail(w, r, e)
		return
	}
	bound, e := a.Engine.Bind(r.Context(), owner(r), n.ID)
	if e == nil {
		e = bound.Rebuild(r.Context())
	}
	if e != nil {
		slog.Warn("Notebook saved; automatic reconciliation will schedule indexing", "event", "notes.scan_enqueue_failed", "error_kind", "dependency")
	}
	notebookSaved(w, r)
}
func (a *App) removeNotebook(w http.ResponseWriter, r *http.Request) {
	if !form(w, r) {
		return
	}
	if !checked(r, "confirm") {
		http.Error(w, "Confirm removal of this mapping", 422)
		return
	}
	if e := a.Repo.RemoveNotebook(r.Context(), owner(r), r.PathValue("id"), revision(r)); e != nil {
		a.fail(w, r, e)
		return
	}
	http.Redirect(w, r, "/notebooks", 303)
}
func (a *App) removeConnection(w http.ResponseWriter, r *http.Request) {
	if !form(w, r) {
		return
	}
	c, err := a.Repo.Connection(r.Context(), owner(r), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if c.Shared && !store.IsAdmin(r.Context()) {
		a.fail(w, r, store.ErrForbidden)
		return
	}
	if !checked(r, "confirm") {
		http.Error(w, "Confirm removal of this connection", 422)
		return
	}
	if e := a.Repo.RemoveConnection(r.Context(), owner(r), r.PathValue("id"), revision(r)); e != nil {
		a.fail(w, r, e)
		return
	}
	http.Redirect(w, r, "/storage", 303)
}
func (a *App) recovery(w http.ResponseWriter, r *http.Request) {
	b, e := a.Repo.Recovery(r.Context(), owner(r), r.PathValue("id"))
	if e != nil {
		a.fail(w, r, e)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="notes-crypt-recovery.json"`)
	w.Write(b)
}

// NotebookCard contains only owner-scoped public settings, never crypt secrets.
type NotebookCard struct {
	store.Notebook
	ConnectionName                        string
	ConnectionReadOnly, EffectiveReadOnly bool
}

func (a *App) notebooksPage(w http.ResponseWriter, r *http.Request) {
	connections, err := a.Repo.Connections(r.Context(), owner(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	books, err := a.Repo.Notebooks(r.Context(), owner(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	recoveries, err := a.Repo.Recoveries(r.Context(), owner(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	v := View{NotebooksPage: true, Connections: connections, Recoveries: recoveries,
		EditNotebook: &store.Notebook{FilenameEncryption: "standard", FilenameEncoding: "base32", DirectoryEncryption: true, Suffix: ".bin"}}
	byID := make(map[string]store.Connection, len(connections))
	for _, c := range connections {
		byID[c.ID] = c
	}
	for _, n := range books {
		c := byID[n.ConnectionID]
		v.NotebookCards = append(v.NotebookCards, NotebookCard{Notebook: n, ConnectionName: c.Name, ConnectionReadOnly: c.ReadOnly, EffectiveReadOnly: !n.Writable(c)})
	}
	if len(connections) > 0 {
		v.EditNotebook.ConnectionID = connections[0].ID
	}
	if connectionID := r.URL.Query().Get("connection"); connectionID != "" {
		if c, ok := byID[connectionID]; ok {
			v.EditNotebook.ConnectionID = c.ID
		}
	}
	v.NotebookConnection = byID[v.EditNotebook.ConnectionID]
	v.EditNotebook.Bucket = v.NotebookConnection.Bucket
	id := r.URL.Query().Get("notebook")
	v.NotebookForm = id != ""
	if id != "" && id != "new" {
		n, err := a.Repo.Notebook(r.Context(), owner(r), id)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		v.EditNotebook = &n
		v.NotebookConnection = byID[n.ConnectionID]
	}
	a.renderNotebooks(w, r, v, books)
}

func (a *App) renderNotebooks(w http.ResponseWriter, r *http.Request, v View, books []store.Notebook) {
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "notebook-dialog-content" && len(v.Connections) > 0 {
		v.NotebookDialog = true
		v.OwnerID = owner(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := a.templates.ExecuteTemplate(w, "notebook-dialog", v); err != nil {
			slog.Warn("Template failed", "event", "notes.render_failed")
		}
		return
	}
	child := &App{Config: a.Config, Engine: a.Engine, Repo: a.Repo, templates: a.templates, Notebooks: books}
	child.render(w, r, v, http.StatusOK)
}

func notebookSaved(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Location", `{"path":"/notebooks","target":"#app-layout","select":"#app-layout","swap":"outerHTML"}`)
		return
	}
	http.Redirect(w, r, "/notebooks", http.StatusSeeOther)
}

// Keep drafts and crypt passwords in the browser. Only safe feedback is returned.
func (a *App) notebookError(w http.ResponseWriter, r *http.Request, message string, status int) bool {
	if r.Header.Get("HX-Request") != "true" {
		return false
	}
	target := r.Header.Get("HX-Target")
	if target != "notebook-feedback" && target != "notebook-dialog-content" {
		return false
	}
	if target == "notebook-dialog-content" {
		target = "notebook-list-feedback"
	}
	w.Header().Set("HX-Retarget", "#"+target)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := a.templates.ExecuteTemplate(w, "notebook-error", View{Message: message}); err != nil {
		slog.Warn("Template failed", "event", "notes.render_failed")
	}
	return true
}

func (a *App) notebookValidation(w http.ResponseWriter, r *http.Request, message string) {
	if !a.notebookError(w, r, message, http.StatusUnprocessableEntity) {
		http.Error(w, message, http.StatusUnprocessableEntity)
	}
}
