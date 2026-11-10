# Notes architecture proposal

Status: design only. S3 with embedded rclone is the likely direction; no backend
or database dependency has been selected or implemented in this repository.

## Product model

A note has a stable opaque ID, title, Markdown body, zero or more tags, and
zero or more attachments. Renaming a note or tag must not change its identity.
The library supports text search and filtering by tag. Reading and editing are
separate views; the editor pairs Markdown source with a rendered preview.
Attachments belong to a note and can be downloaded or linked from its body.
The first iteration is private to its owner; sharing and collaboration are outside
these mockups. A title and body can exist without tags or attachments.

## Platform boundaries

- Trust `X-Auth-Request-User` only behind the existing gateway/Caddy authentication
  boundary. Use the verified UUID for ownership, never a display name.
- Scope note, tag, attachment, search, and storage configuration lookups to that
  owner, including downloads and references inside rendered Markdown.
- If PostgreSQL is selected for metadata, use a dedicated broker-provisioned
  database (`/v1/instances/postgres/notes/uri`), with owner-scoped credentials and
  connection configuration. This is a proposed path, not a deployed endpoint.
- Notes owns its storage settings. Reuse Files' integration patterns, not its
  private database tables or credentials. Cross-service connections require an
  explicit future API and authorization design.
- Keep service files in this repository. Routing, image selection, and deployment
  integration require separate changes in the owning parent/infra repositories.

## Candidate storage layout

With embedded rclone S3 APIs, a dedicated prefix could look like this:

```text
<configured-prefix>/<owner-id>/notes/<note-id>/
  note.md
  metadata.json
  attachments/<attachment-id>/<safe-filename>
```

`note.md` remains portable Markdown. `metadata.json` could carry a schema version,
title, stable tag IDs/labels, attachment descriptors, and revision. This is an
illustration, not an on-disk contract. Use ID-based object keys; validate paths and
attachment references rather than accepting arbitrary object keys from the browser.

The storage settings screen illustrates a connection, bucket, and Notes-specific
prefix. It does not contact a provider or validate credentials. Encryption, if
selected later, should use rclone crypt rather than custom cryptography. Preserve
Files' connection-time egress controls and operator-managed endpoint exceptions.
Do not introduce subprocesses, an rclone RC server, or a local sync mirror.

## Consistency and failure behavior

- Choose an authoritative source for titles, tags, attachment manifests, and
  revisions before implementing indexing. PostgreSQL indexes must be rebuildable
  if S3 manifests become authoritative; a database-first design needs an explicit
  export/recovery contract. Do not silently maintain two sources of truth.
- S3 writes and PostgreSQL transactions are not atomic together. Define retryable
  pending writes, reconciliation, and orphan cleanup before storing content in S3
  and indexing it in PostgreSQL. Upload completion must precede publishing a link.
- Use optimistic concurrency for saves. Detect stale revisions and offer review
  of both versions; never silently overwrite another tab's edits.
- Preserve the draft on save/upload failure and make unsaved state visible.
  Changing tags or removing attachments must participate in the revision policy.
- Define note deletion and attachment cleanup together. Do not remove an object
  still referenced by a committed revision; consider delayed cleanup.
- Read-only connections must disable every mutation, including tag changes,
  note deletion, and attachment uploads/removal.

## Markdown and attachments

Select a maintained Markdown parser and HTML sanitizer when building the backend.
Disable raw HTML by default; restrict link schemes and sanitize rendered output.
Do not load remote images by default without a deliberate privacy/egress policy.
Resolve attachment links through owner-checked routes. Browser-supplied MIME types
and filenames are untrusted. Apply limits to body size, attachment size/count,
and total request size. Serve active formats such as HTML/SVG as downloads unless
an isolated preview policy exists. Never expose S3 credentials or presigned links
in logs. Any future public sharing needs a separate authorization design.

## Decisions still open

1. S3 or PostgreSQL authority for document bodies and metadata; export format.
2. Per-user storage connections versus an operator-managed bucket.
3. Markdown dialect, attachment reference syntax, and search indexing strategy.
4. Save behavior (explicit save versus autosave), revision history, and recovery.
5. Tag normalization, rename/merge behavior, and storage encryption requirements.

The mockups use explicit save and tags without folders to make these flows
reviewable. They do not settle the backend decisions above.
