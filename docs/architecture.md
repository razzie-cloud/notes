# Notes architecture and API

The [plan](plan.md) is implemented with the user's storage correction: Notes takes
S3 credentials in its UI, following Files, with optional rclone encryption.

## Browser navigation

Library, notebook selection, tags, pagination, reader/editor and settings links
use locally scoped HTMX navigation. Each updates `#app-layout`, including the
sidebar's active state, counts and notebook context, while the header and dialog
controllers remain in place. The service link follows the current notebook and
the document title follows the returned page. Direct URLs still render complete
pages. Downloads, attachments, external links and new-tab actions remain native;
relative Markdown links to other notes use the reader panel.

History snapshots stay disabled. Back/Forward fetch fresh HTML for the same
layout region. An unsaved editor guards panel changes and history traversal as
well as native page exits. Canceling retains its draft and URL; obsolete requests
cannot overwrite a newer view. Preview listeners initialize after swaps and
pending preview work stops when its editor is removed. Explicit saves and
attachment mutations retain their normal form flow. A rendered save result uses
the equivalent GET editor URL so subsequent history restores can fetch it.
Navigation errors retain the current page/draft with a native reload link for
retrying or restarting sign-in.

Run the opt-in browser regression with an installed Chromium browser:

```sh
NOTES_BROWSER_TEST=1 CHROMIUM_EXECUTABLE_PATH=/path/to/chromium \
  go test ./internal/app -run TestNotesNavigationBrowser -v
```

It renders production templates/assets with local request fixtures, covering
draft guards, fresh history, overlapping navigation, native saves, preview
initialization and auth-error recovery. It does not verify live SSO or S3.

## Storage configuration

A **connection** belongs to a gateway UUID and contains provider, endpoint, region,
path-style option (off for new connections), optional fixed bucket, read-only policy and credentials (access key, secret key,
optional session token). **Conditional writes** defaults on for new and existing
connections. Its personal owner or a shared connection’s administrator can disable it for a provider without S3 write preconditions.
A **notebook** belongs to a gateway UUID and references an owned personal or shared
connection plus bucket, prefix, read-only policy and optional crypt configuration.
Multiple connections/notebooks are supported. Notebook/content queries include owner and ID; connection reads admit owned or shared records;
knowing a notebook ID does not authorize content or settings access.

Connections and notebooks are separate Notes-owned PostgreSQL records. Files'
tables, directories and credentials are never shared implicitly. The service
starts without storage configuration. Connections are added through `/storage`;
there is no deployment-wide bucket, provider credential or S3 secret volume.

The first connection form is inline. With existing connections, Add/Edit loads
the form through HTMX into an HTML5 dialog; direct links retain a full-page form.
The empty home page offers notebook setup when an owned personal or shared
connection is available; it offers connection setup only when none are available.
Errors update feedback while keeping the draft in the browser. Successful saves
refresh connection settings and close the dialog. Escape and Cancel discard the
dialog draft and restore focus. Connection locations are disabled while notebooks
are attached, with hidden original values preserving the existing POST contract.

Notebooks have a separate sidebar page at `/notebooks`. With no notebooks, the
first form stays inline; without a connection, the page links to connection setup.
Existing notebooks show cards with connection, bucket/prefix, effective access,
encryption and crypt recovery downloads. Add/Edit loads a native dialog through
HTMX, with full-page forms available at `/notebooks?notebook=new` or an owned ID.
The legacy `/storage?notebook=<id>` URL renders the notebook edit page as well.
Notebook POST routes and revision checks are unchanged; successful saves return
to `/notebooks`, refreshing its shell and closing any dialog. Errors return only
safe feedback and keep the draft and crypt passwords in the browser. Cancel and
Escape clear dialog drafts and restore focus. Retained recovery records live on
the Notebooks page, including records whose mappings were removed.

Connection credential fields are never prefilled in HTML or returned by normal
queries. Blank inputs preserve saved values; clearing a session token is explicit.
Changes use revision checks. Location fields (provider, endpoint, region and
addressing mode) cannot change while notebooks are attached, avoiding stale search
from a different backing store. Notebook locations and crypt options are immutable;
only name and read-only policy can change. Prefixes on the same connection/bucket
cannot overlap within one user’s notebooks. Users with multiple connections to the same physical prefix must
manage that overlap themselves, as in Files; S3 preconditions still protect edits.

Connection read-only overrides notebook policy. Each operation resolves current
owner-scoped notebook/crypt settings and owned-or-shared connection settings under shared row locks, held until its S3 work ends.
Updates/removals wait for in-flight operations; old handles cannot bypass committed
policy changes. Indexing and mutations reuse their existing PostgreSQL transaction
for these checks, avoiding nested pool acquisition while holding an object lock.
Readiness probes check PostgreSQL, not arbitrary user-controlled S3 endpoints.

Deleting a notebook removes only its settings and derived index/progress; it never
deletes S3 objects. Crypt recovery records survive removal and remain owner-scoped.
Connections cannot be removed while notebooks reference them.


## Shared S3 connections

Personal connections remain owner-managed. With `ACCESS_GATEWAY_URL` configured,
users whose verified gateway assertion has `admin=true` see a **Shared** switch
when adding a connection. It defaults off. Shared connections are available to
all users of this service, but only current administrators can add, edit or remove
them. This also applies to the original creator after demotion. Shared status
cannot change after creation; the database enforces this invariant. Legacy proxy
mode without signed gateway verification never grants shared-management rights.

A shared connection also has **Add user ID as a prefix**, initially off. With it
on, a mapping resolves to `s3://<bucket>/<gateway-uuid>/<mapping-prefix>/`; an empty
mapping prefix resolves to the user's UUID root. The UUID root sits outside
rclone encryption, preserving compatible crypt layouts within each user's root.
This switch cannot change while any user has an attached mapping. Detach mappings
to change it; no S3 objects are moved or deleted. Prefix-off connections allow
users mapping the same bucket/prefix to access the same objects.

Mappings, encryption passwords and recovery records remain personal. Users never
receive the shared S3 credentials. Files and Notes own independent connection
records: sharing within one service does not copy settings into the other.
The notebook form previews the effective path when choosing a connection.


### Fixed buckets and connection actions

Every connection card has **Use connection**, which opens directory/notebook
setup with that connection selected. Connection setup includes **Fixed bucket
(optional)**. Leave it blank to choose a bucket for each mapping. When set, the
mapping form fills that bucket and makes it read-only; changing the connection
updates this field and restores a draft bucket when returning to an unrestricted
connection. Handlers, storage constructors and locking SQL triggers reject bucket
overrides, including bucket-access tests. Omitted bucket values resolve to the
fixed bucket on the server.

A connection can be restricted to a bucket only if every existing mapping, across
all users, already uses it. Incompatible restrictions return a conflict. Clearing
the restriction preserves each mapping's saved bucket and never moves S3 data.

New connections default **Use path-style bucket addressing** off. Existing saved
choices are preserved. Enable it for providers that need it, including the local
`http://s3-demo:9000` fixture.

## Optional encryption and egress

Notes uses rclone's exported `crypt.NewCipher`, name/size translation and streaming
EncryptData/DecryptData implementation. This is standard rclone crypt, with no
custom cryptography. Standard or off filename encryption, directory-name encryption,
base32/base64/base32768 encodings, suffix, password and optional second password
follow Files' options. There is no subprocess, RC server, local mirror or production
rclone filesystem cache alias. Independent rclone filesystem tests verify reads and
writes in both directions, including filename encryption off.

The AWS Go SDK handles paginated ListObjectsV2 and conditional PUT/GET/DELETE because
Files' higher-level rclone FS abstraction does not expose those operations. The
mapping layer translates physical S3 keys to owner-bound logical keys and sizes;
ETags remain opaque **underlying S3 validators** for both encrypted and plain data.
A ciphertext change gets a new ETag even when the plaintext remains the same.
Binary content is never downloaded for attachment indexing.

Custom S3 endpoints use rclone-compatible checksum defaults: calculate/validate
AWS-specific checksums only when required, avoiding optional CRC/trailer headers
that compatible providers may reject. PUT still supplies Content-MD5 for transfer
integrity and, by default, preserves If-Match / If-None-Match. Unsupported requests
fail rather than silently falling back. Safe diagnostics also classify unsupported headers
named in bounded provider error XML without logging the raw response.

With **Conditional writes off**, each note/attachment uses a connection-scoped row
in `object_write_versions`, owned by the connection creator and keyed by effective
bucket plus the exact mapped physical S3 key, including prefixes and rclone filename
encryption. Authorized notebooks on the same connection that resolve to the same
object share this row, even across users and replicas. It grants no storage access;
every operation first rechecks notebook ownership and current policies. Reads issue
an opaque `"notes-db-<revision>"` ETag. Mutations take the same PostgreSQL per-object
transaction lock as indexing and then a separate physical-object lock after policy
row locks, all on the caller's transaction. HEAD/GET take this physical lock too.
Mutations read current S3 metadata, compare the client token
with the current database revision, and claim a new UUID with an SQL
compare-and-swap before writing. Every successful save advances the revision even
when the content and S3 ETag are unchanged. Deletes retain a revision tombstone;
recreation cannot reuse an old editor token. The actual upload/delete omits S3
write-condition headers but still checks current storage first. GET preconditions
and underlying S3/ciphertext validators used by indexing remain intact.

Completed external S3 edits detected by HEAD invalidate database tokens. Providers
may omit version IDs on PUT; learning a previously missing version ID on HEAD
does not spuriously invalidate the token returned by that save. Changing the
connection's conditional-writes setting invalidates its earlier DB revisions;
reload existing editors after switching modes. No replica caches this policy.
Removing a notebook retains its physical revisions for other mappings and a later
reattach to the same unchanged object. Saving a detached connection clears retained
revisions because its location may change. Deleting the connection cascades them;
a recreated connection has a new UUID and revisions.

Database coordination protects mappings using the same connection/database.
Independent connection records, other Notes installations, Files and external S3
clients do not share these locks. An external change between HEAD and upload/delete can be overwritten
or deleted without a conflict. Without retained object versions or backups, that
content may be unrecoverable. Notes does not automatically enable bucket versioning.
PostgreSQL and S3 are not atomic: a failed database commit after an S3 success can
invalidate the returned write token, requiring a fresh read before another edit;
it never changes an already successful S3 save into a failed save response.

An encrypted mapping must either target a verified **empty raw prefix** or decrypt
a known nonempty existing file successfully before being saved. Raw listing prevents
a wrong password from hiding pre-existing data during initialization. Password/options
changes are not an in-place re-encryption feature. Crypt recovery downloads include
passwords, salt and layout options, never the S3 access/secret keys. They are explicit
owner-only attachments with no-store responses. Preserve them securely.

Crypt encrypts data at the S3 provider. Notes decrypts on the server; credentials,
crypt passwords and plaintext search text live in PostgreSQL, as Files' secrets do.
This is not browser-only encryption. No additional application master key is used.

The SDK transport follows Files' connection-time egress controls: disable environment
proxies/redirects, resolve once and validate every returned address, and dial a
validated numeric address. Private/reserved IPs and non-443 ports are blocked by
default; exact operator host:port exceptions permit configured private endpoints.
HTTP needs both an exact exception and S3_ALLOW_HTTP. User-entered connections do
not change this policy. Custom private endpoints generally need path-style addressing
so the destination matches the exact operator exception.

## Content layout and rebuilding

```text
<notebook-prefix>/notes/<relative-path>.md
<notebook-prefix>/attachments/<relative-path>.md/<filename>
```

Crypt transparently transforms the portion below the configured prefix. Prefixes
are user-selected dedicated namespaces; there is no extra physical gateway UUID
segment. Ownership instead comes from the notebook settings. Internally keys have
an owner UUID prefix; the storage layer refuses keys belonging to another owner.
The index scope is the notebook ID, with composite scope/owner foreign keys.
Removing a notebook cascades its index but leaves other owners/notebooks unaffected.

Keys are case-sensitive, valid UTF-8 and limited to 768 relative bytes. They cannot
contain empty, `.` or `..` segments, leading/trailing slash, backslash or control
characters. Physical S3 keys, after encryption/prefix translation, are checked
against the 1024-byte S3 limit. Only recognized notes/attachments are indexed.
Directory markers are ignored. Invalid encrypted names fail the scan, so they
cannot be mistaken for absent notes during deletion reconciliation.

Markdown is ordinary UTF-8 with optional YAML front matter for `id`, `title`, `tags`
and `created`. Unknown fields are allowed. IDs are optional portable metadata, not
lookup or authorization keys. Without title, use the first `# ` heading, then the
filename. Indexing never rewrites imports. Title is limited to 1024 bytes, ID to
256, tags to 100 with 128 bytes each; front matter is limited to 64 KiB. Invalid or
oversized imports get a classified index_error; API writes reject malformed input.

Search uses weighted `simple` PostgreSQL vectors and GIN indexes, preserving
Hungarian/Unicode tokens without English stemming. Rank precedes modification/key
ordering. The title and first 65,536 body characters are searchable to stay below
vector limits; direct Markdown reads and stored content remain complete. Tags are
trimmed, deduplicated and case-sensitive. No binary text extraction/backlinks exist.

Attachments belong to a note path, have independent validators, and remain stored
when a note is deleted/renamed. Their owner can retrieve/delete them by API even if
the note is absent. All binary formats, including HTML/SVG, are forced downloads
with application/octet-stream and nosniff. The explicit-save UI follows the
reviewed mockups: sidebar, library rows, tag collections, reader, split Markdown
editor and contextual connection and notebook forms. Goldmark renders reading and live preview
with escaped text, raw HTML omitted, and images reduced to alt text. Links allow
HTTP(S)/mailto, relative Markdown notes and note-owned attachment downloads;
other schemes and absolute app paths are omitted. Content headings start at h2
to retain one page-level h1. Preview performs no S3 write. Title and tag controls
merge into the original YAML tree, preserving unknown fields, IDs and timestamps.
Reading/indexing never rewrites imported content. Save failures retain title, tags,
body and the original write token; the unsaved-draft warning also survives the
failed response. Attachment forms require saving a dirty note first. Removal uses
a current storage write validator, including database revisions in compatibility
mode. Library/attachment lists and tag counts remain derived and eventually
consistent; tag navigation shows up to 200 most-used tags.

S3 holds authoritative content/front matter. PostgreSQL contains **two kinds of
state**: durable `storage_connections`, `notebooks`, `storage_secrets`; disposable
`objects`, `index_scans`, `scan_seen`, and River tables. Back up durable settings
and gateway identities. `object_write_versions` is persistent write-coordination
metadata, kept separately from the disposable index. Index rebuilds preserve it.
It can be reconstructed from S3 with fresh UUID revisions after loss, invalidating
all earlier editor tokens instead of accepting stale edits. Rebuilding the index does not require deleting settings.
After a full database loss, restore its settings backup or re-enter credentials,
bucket/prefix and exact crypt parameters/recovery passwords. Attach schedules a
scan automatically; existing notes/attachments recreate search without a database-
only content manifest. Losing crypt passwords can make ciphertext unrecoverable.

Migration 00006 deliberately expires only compatibility-mode write tokens and
moves their foreign key from notebooks to connection UUIDs. This requires a stopped
writer upgrade: back up settings and preserve drafts, stop and drain **all** Notes
HTTP processes and River workers, migrate once, then start only the new release.
Reload compatibility-mode editors; old tokens conflict. Mixed old/new replicas are
unsafe because they coordinate different identities. Rollback also stops/drains
all processes, runs the Down migration, restarts the old release and reloads editors;
rolling back the binary alone is unsupported. Downgrade restores notebook-only
coordination, so avoid concurrent compatibility-mode writes through shared aliases.
Neither direction changes settings, secrets, indexes, jobs or S3 content.

Migration 00002 adds this ownership model and clears only the previous operator-
bucket prototype's derived index. No S3 writes occur in migration. To attach old
prototype data, enter credentials in Connections and set its notebook prefix to
`<old-prefix>/<gateway-user-uuid>`, preserving the old notes/attachments layout.
Old queued scope IDs resolve to no notebook and finish without accessing storage.

## Jobs and consistency

River uses the same PostgreSQL database/pool as the service. Goose migrations have
a session lock; River migration versions retain their own transaction boundaries
under a separate session lock, because enum migrations require intermediate commits.
The `maintenance` queue has one worker per replica; `indexing` has INDEX_WORKERS
bounded workers per replica. All replicas share database settings rather than
operator S3 environment configuration.

A leader-scheduled periodic job, with RunOnStart, enumerates notebooks in durable
200-row pages and transactionally enqueues per-notebook scans. This runs on startup
and roughly every SCAN_INTERVAL (default five minutes). The attach handler also
queues a scan. The open-source periodic schedule restarts after leader changes;
persisted jobs and scan progress survive. Failed immediate insertion is repaired
by periodic scheduling. Deleted notebook jobs finish without accessing S3.

Unique active scan jobs reduce duplicate enqueueing; a per-notebook advisory lock
also serializes scan pages across replicas. Listing pages hold at most 200 objects;
seen keys, cursor and index jobs commit together. An incomplete/failed listing
never authorizes deletions. Only its final successful page transitions to a durable
sweep, which enqueues checks for at most 200 missing indexed keys per page. Sweep
never directly deletes rows. After sweep completion, seen records are removed.
A scan marked complete has **enqueued** its work; indexing may still be pending.
Workers snooze after 20 pages, retaining progress without consuming retries.

Object jobs serialize with writes using per-notebook/key locks. They HEAD current
objects, compare ETag/size/last-modified/version with the index, skip unchanged
content, and read Markdown conditionally with If-Match. A final HEAD revalidates
before commit. Missing version IDs on GET are tolerated when HEAD verifies them.
Only authoritative S3 absence removes rows; permissions, crypto failures, timeouts
and transient failures do not imply absence. There are no stale payloads in jobs
and ETags are never ordered. Jobs are cancellation-aware and retry-safe.

LIST is not a transactional snapshot. Objects added behind a cursor or changed
after final HEAD may remain missing/stale until a later successful scan. Missing
candidates are HEAD-checked, protecting concurrent creations omitted by LIST.
External writers do not take PostgreSQL locks; changes between final revalidation
and commit remain an eventual-consistency window. S3 ABA without distinct versions
cannot be distinguished. Periodic reconciliation repairs gaps without events.
SDK retries are bounded to three attempts; River jobs try eight times before
becoming discarded. Later scans resume progress and enqueue fresh object work.

Successful S3 saves remain successful even if queue insertion/commit fails; the
response reports index pending and periodic scans repair gaps. Queue insertion
uses a savepoint so an indexing SQL error cannot roll back a successful mutation's
database revision. Search stays
available with partial/stale results during rebuild. Choose concurrency/interval
for bucket size: scans list recognized objects and queue HEAD checks, so operators
must keep the backlog bounded. The service enables no versioning or destructive
lifecycle policy; frequent saves can accumulate noncurrent versions if enabled
outside Notes. Shutdown drains HTTP and River before closing database pools.

## HTTP API

Every app route requires a trusted single canonical gateway UUID header. With
`ACCESS_GATEWAY_URL` configured, startup fetches the gateway public keys and
requests additionally require `X-Auth-Request-Token` with a valid v4.public
signature, issuer, app audience, lifetime, method/URI binding and matching UUID/name.
Expired sets refresh on use; discovery failure cannot extend cached trust. All
admin actions use `gatewayidentity.IsAdmin`, propagated through `store.WithAdmin`,
as in Files. Both `/v1/admin/*` and shared-connection management require signed
`admin=true`; legacy proxy mode never grants admin rights. Unsigned role headers,
including the obsolete `X-Notes-Admin` marker, grant no authority. Caddy strips
incoming identity and protects every path/callback with ordinary `/forward_auth`,
copying the gateway's signed identity before proxying. No path-specific proxy
admin rule is required. Usernames do not grant permissions.
Browser writes require the exact configured Origin; cross-site/null origins are
rejected. Referrer-Policy is same-origin so ordinary forms carry a valid Origin.
Cookie-free API clients can omit Origin behind the proxy. Responses use no-store,
nosniff and a restrictive CSP. Failed requests and editor saves include an
X-Request-ID and structured, allowlisted S3 operation/status/error-code diagnostics.
Raw backend messages, object keys, note bodies and credentials are never logged.

Content API calls select a notebook with `?notebook=<id>` (optional when exactly one
is configured). Selection is owner-checked before either index or S3 access. The UI
keeps the notebook in navigation, search, forms and downloads. Admin operations are
also notebook-scoped; they do not grant access to another owner's configuration.

| Method / path | Behavior |
| --- | --- |
| `GET /note?key=<path>.md` | Authoritative rendered reader, scoped attachment downloads |
| `GET /edit?key=<path>.md`, `POST /save` | Split editor and explicit save with draft retention |
| `GET /tags` | Notebook-scoped tag collections |
| `POST /preview` | Bounded Markdown-to-HTML preview, without persistence |
| `POST /attachments/delete` | Owner/notebook-scoped conditional attachment removal |
| `GET /v1/notes?q=words&tag=exact&limit=50&offset=0` | Indexed JSON; max 100 results, max offset 1,000,000 |
| `GET /v1/notes/<path>.md` | Authoritative Markdown, write ETag, S3 version ID |
| `PUT /v1/notes/<path>.md` | Raw UTF-8; If-None-Match: * creates (201), quoted If-Match updates (200) |
| `DELETE /v1/notes/<path>.md` | Conditional delete (204), attachments retained |
| `GET /v1/attachments?note=<path>.md` | Indexed list, first 100 keys with sizes/ETags |
| `GET/PUT/DELETE /v1/attachments/<path>.md/<filename>` | Bounded download, conditional binary upload/delete |
| `POST /v1/admin/rebuild` | Admin: enqueue/resume scan (202) |
| `POST /v1/admin/rebuild?restart=true` | Admin: reset only scan cursor, retaining settings/search/S3 |
| `GET /v1/admin/index` | Admin: scan status/counters/errors and pending/discarded jobs |
| `GET/HEAD /healthz`, `/readyz` | Private liveness / cached PostgreSQL readiness |

Missing write preconditions return 428; stale ones return 412 with current ETag if
available. Read-only mutations return 403. Cross-owner resource selection returns
404. Connection and notebook forms at /connections and /notebooks use POST and configuration
revisions (409 on conflict). /recovery/<id> downloads owner-only crypt recovery,
including records retained after detachment. These settings routes require normal
owner authentication, not the gateway admin role.

API clients must treat ETags as opaque and use the latest GET/save response for
If-Match. With conditional writes off this is the database revision, not the S3
ETag shown in the derived listing/search rows. Missing preconditions remain 428;
stale database revisions return 412 and preserve editor drafts, just as in strict
mode. Creation still uses If-None-Match: * at the HTTP API in either mode.

Readiness checks PostgreSQL every ten seconds with a two-second deadline and a
thirty-second expiry. S3 failures surface in operations/jobs and aggregate admin
status, not global bucket probes. Real provider conditional-write support and live
Swarm/gateway acceptance still need a dedicated operator environment. See
[verification](verification.md) for completed automated and browser checks.
