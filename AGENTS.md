# Notes development guide

- Independent Go service repository, included as the parent's `notes/` submodule.
  Keep source here and deployment coordination in parent/infra.
- Read `docs/architecture.md` and the user correction in `docs/plan.md`. Storage
  setup follows Files: personal or admin-managed shared S3 connections, then bucket/prefix notebooks
  with optional rclone crypt. Notes owns its settings; no Files tables are shared.
- S3 is authoritative for Markdown/front matter and attachments. PostgreSQL
  connection/notebook/crypt recovery records are durable and need backups. Only
  objects, scans and River jobs are disposable. Do not describe the entire
  database as rebuildable or restore the superseded operator-bucket configuration.
- Scope notebook/content/crypt lookups by gateway UUID and notebook ID.
  Connection reads allow owned personal or shared records; only a signed gateway
  admin may create/edit/remove shared connections, including a demoted creator.
  Shared status is immutable. Shared-only user-ID prefixing locks while any user
  has an attached notebook. Keep the UUID outside crypt, export the effective
  recovery prefix, and invalidate conditional-write revisions for all attached
  notebook owners on a shared policy change. Preserve database scope triggers.
  Background discovery may enumerate settings internally; workers resolve their
  current owner and configuration before each job. Admin APIs require the
  dedicated gateway admin route and a stripped/reissued X-Notes-Admin marker.
- Use rclone's exported crypt APIs, never custom encryption. Conditional S3
  writes default on. Only the personal owner or a shared connection’s admin may disable them;
  then use owner-scoped PostgreSQL revision compare-and-swap and per-object
  locks, independent of the search index. Preserve opaque ciphertext ETags for
  storage/indexing and issue separate DB validators to editors/API clients.
  External writers can race the non-atomic check/upload in compatibility mode.
  Both encrypted and plain storage keep concurrency protection; S3 success
  remains successful if enqueueing fails. Isolate queue errors with a savepoint
  so they cannot roll back a successful write's revision record.
- Preserve connection-time egress checks, exact private endpoint exceptions,
  disabled redirects/proxies, and bounded downloads. Runtime uses no rclone
  subprocess, RC server, global fs aliases, application master key or secret volume.
- Per-object/page transactions and storage policy checks share a connection.
  Hold current settings row locks through S3 operations; read-only connection
  policy overrides notebook policy. Never cache credentials/policy across calls.
- Settings revisions prevent stale edits. Notebook physical/crypt settings are
  immutable; connection locations cannot change while notebooks are attached.
  Removing mappings cascades derived rows only; retain crypt recovery records
  and S3 objects. Never render stored credentials/passwords into normal forms.
- Scan cursors and jobs commit together. Only complete listings permit sweep;
  current HEAD is authority for absence. Preserve locks, conditional reads,
  revalidation, per-notebook isolation and cancellation/retry safety.
- Embedded PicoCSS/HTMX, local licenses and unchanged logo. Escape Markdown, no
  raw HTML or remote images. Binary options use Pico checkbox role=switch.
  Keep static mockups separate from live writes.
- Use the shared `static/account-header.css`, identical to Gateway and Files:
  shield, gateway-linked username and inline Administrator badge only for the
  verified signed admin role. Use public `gatewayidentity.AccountURL`, never the
  private discovery URL, and synchronize its helper/tests with Files. Settings
  stay in sidebar navigation. See parent docs/brand/README.md's header contract.
- Run gofmt, `go build ./...`, `go vet ./...`, `go test ./...`,
  `go test -race ./...`. Integration uses disposable loopback TEST_DATABASE_URL
  with random schemas and in-memory S3. Never test against production backends.
- Publishing, pushing and deploying require authorization. Report checks and
  limitations; Notes and landing images must be published before selecting tags.

When ACCESS_GATEWAY_URL / --access-gateway-url is configured, initialize
internal/gatewayidentity before serving. Require X-Auth-Request-Token on every
management route, including static assets, and verify its v4.public signature,
issuer/audience/time, exact method/URI and UUID/name header equality. Cache public
keys only until expiry; discovery outages must not extend trust. Keep the verifier
and tests synchronized with the other S3 app's independent copy. Empty gateway URL
preserves trusted-proxy mode; public Files capabilities and health probes bypass
identity. Notes admin APIs also require signed admin=true and the proxy marker.

Broker discovery uses BROKER_TOKEN_FILE / --broker-token-file and optional
BROKER_CA_FILE / --broker-ca-file. Discovery URLs carry only public client IDs.
Keep internal/brokerdiscovery and its tests synchronized across first-party apps.
Never log discovery URLs or raw HTTP errors; no authentication fallback is allowed.

Connections support an optional fixed bucket, on personal and shared records.
Use `Connection.BucketFor` before mapping validation/storage access and under the
connection row lock when saving mappings. Never trust the read-only form field.
SQL triggers reject mapping overrides and connection restrictions incompatible
with any user's existing mappings. Clearing a restriction retains saved mapping
buckets. New connection forms and SQL defaults use path_style=false; retain
existing saved choices and explicitly enable it for the local S3 demo.

Use **connection** for saved S3 provider settings in UI labels, help, errors and
documentation. Reserve **storage** for the data, provider or physical location;
do not use it as another name for a connection. Preserve existing routes, form
fields and database identifiers when changing wording.
