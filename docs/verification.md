# Implementation verification

Verified on 2026-11-10 using Go 1.27, disposable loopback PostgreSQL 14 with data
under `/tmp`, random integration schemas, and in-memory S3. No production data,
Docker context, live deployment or external provider was changed.

## Consistent signed admin authorization (2026-11-19)

Notes maintenance APIs now use the same `gatewayidentity.IsAdmin` role check as
Files and both apps' shared connections. The old `X-Notes-Admin` marker grants no
authority. Legacy proxy mode denies all admin actions. Parent Caddy and Swarm
labels use ordinary `/forward_auth` for every Notes path, including callbacks.
Publish/select an updated Notes image before applying the simplified proxy rules.

Validation used Go 1.27, a temporary loopback PostgreSQL 17 container with random
test schemas, and the in-memory S3 fixtures:

- `gofmt`, `go build ./...`, `go vet ./...`, `go test ./...` and
  `go test -race ./...` passed in Notes. Regression cases cover missing signatures,
  members with forged role headers, legacy-mode rejection, signed admin access
  without a marker, read-only notebook maintenance and cross-owner denial.
- Parent Compose validation passed with `.env.example`, both with and without
  the `storage-demo` profile. Swarm Compose validation passed with the example
  environment and placeholder image tags. Notes and Files management route labels
  match exactly, and signed verification remains enabled. No deployment was performed.
- The cached Caddy image adapted the real parent Caddyfile successfully. Twelve
  isolated loopback cases using that generated Notes route and auth/backend stubs
  verified identity stripping/copying, anonymous redirects, callback protection,
  original method/URI preservation and ordinary auth for maintenance APIs.
- `git diff --check` passed in the parent and affected submodules. The independent
  Files/Notes verifier implementations remain identical apart from their comments.

The opt-in browser navigation test was skipped. No real passkey/SSO flow, live
Swarm rollout or external S3 provider was exercised. The routing stubs check
proxy transport; actual PASETO/role enforcement is covered by Notes tests.

## Separate notebook settings page (2026-11-11)

The reviewed notebook concept is implemented at `/notebooks`, separately from
Connections. Empty storage links to connection setup; the first notebook form is
inline, while populated pages use cards and Add/Edit dialogs. Saves refresh the
notebook list, and errors retain browser drafts. Advanced crypt options, recovery
downloads, immutable locations, owner scoping and revision checks remain.

Validation used a fresh loopback PostgreSQL cluster, random integration schemas
and in-memory S3:

- `gofmt`, `go build ./...`, `go vet ./...`, `go test ./...`, and
  `go test -race ./...` in Notes. Regression coverage includes empty/list/fallback
  forms, selected sidebar state, escaped metadata, creation fields, safe error
  fragments, encrypted creation, effective read-only policy, owner isolation,
  immutable settings, stale revisions, redirect destinations and recovery
  retention after removal.
- Chromium checked 48 layouts and eight complete real-handler workflows across
  light/dark at 320, 390, 768 and 1440px. Checks covered one primary heading,
  loaded branding, unique IDs, overflow, dialog focus/return, Cancel/Close/Escape,
  encryption controls after swaps, validation draft retention, real saves,
  connection location locks and recovery downloads. One deliberately aborted
  save verified network-error draft retention. The final pass had no page or CSP
  errors; desktop/mobile screenshots under `/tmp` were inspected visually.
- A JavaScript-disabled Chromium context verified ordinary full-page notebook
  create/edit/remove and fallback forms. Parent and Notes `git diff --check`
  passed. The temporary browser harness was removed.

No external storage provider, gateway login flow, image publication or deployment
was exercised. The checked-in mockups remain separate design references.

## User-managed storage correction

The operator-bucket implementation has been replaced with Files-style setup:
owner-scoped connections, bucket/prefix notebooks, optional rclone crypt and
PostgreSQL-backed credentials/recovery. PostgreSQL settings are durable; only
content index, scan progress and jobs are rebuildable.

Checks performed:

- Notes: gofmt, `go build ./...`, `go vet ./...`, `go test ./...`,
  and `go test -race -timeout 180s ./...` with TEST_DATABASE_URL set.
- New tests cover connection setup and blank-secret retention; storage/recovery
  owner isolation; settings revision conflicts; wrong crypt password rejection;
  refusal to initialize an occupied prefix; encrypted/plain CRUD; ciphertext
  preconditions; read-only connection precedence on old handles; detach/recovery
  retention; reattach verification; encrypted import, update, delete and rebuild
  after wiping only derived tables; and transaction reuse with a one-connection
  pool. Existing scan/race/retry/Unicode/search tests remain in the suite.
- Independent rclone S3+crypt filesystem interoperability: rclone reads Notes'
  ciphertext, Notes reads rclone-written files, and both listing/metadata paths
  work. Tested standard and off filename encryption with base64 encoding; normal
  encrypted service tests use base32. No custom encryption is implemented.
- Chromium against the running Notes service with **no provider credentials in its
  environment**: first-use setup, add S3 connection, create encrypted notebook,
  explicit save/conflict with preserved draft, encrypted attachment upload/download,
  secret-free normal forms, owner-only recovery, detach preserving content/recovery,
  wrong-password rejection, verified reattach, eventual Hungarian search, notebook
  selection, read-only connection enforcement, light/dark themes and 390/1440px
  content/settings layouts. No JS/CSP errors. Browser checks caught and fixed
  select labels whose accessible names included their option text.
- Parent: `docker compose --env-file .env.example config --quiet` passes without
  Notes provider credentials. Swarm: `make -C infra ENV_FILE=.env.example config`
  with placeholder image tags and DOMAIN=example.test passes without a Notes
  bucket setting or provider Docker secrets. deploy.sh no longer requires them.
- `bash -n infra/deploy.sh scripts/update-image-tags.sh`, and `git diff --check`
  in the parent and changed submodules.

## Earlier vertical-slice verification retained

The preceding implementation also passed Files build/vet/full tests against the
same disposable PostgreSQL, plus `go test -race ./internal/devs3` for its conditional
S3 development-fixture change. That Files code is unchanged by this correction.
The landing Notes card/navigation passed both themes at 320–1440px. Generated
local Caddy routing was tested with an auth stub: incoming identity/admin markers
are stripped, verified identity is copied, members are denied admin APIs, and
approved admins succeed. This records the earlier proxy-marker policy; the signed
admin authorization section above supersedes its admin routing.

## Environment limits

The Notes image was rebuilt locally for save-failure diagnostics; no image was
published, GitHub Actions was not run, and no live Swarm or real gateway passkey
flow was exercised. No diagnostic objects were written to the real S3 provider.
CI is configured for PostgreSQL 17, race tests and image building. Verify conditional
PUT/DELETE on the actual provider before relying on edit preconditions. S3 remains
authoritative for content; durable settings and crypt recovery still need backups.
See [architecture](architecture.md) for consistency and recovery boundaries.

## Save-failure diagnostics

The editor previously returned 503 without recording the failed write. Failed
editor saves now include a request ID and structured, allowlisted S3 operation,
HTTP status, API error code and feature category. Raw backend messages, signed URLs,
object paths and note content are discarded. Unit/integration tests assert that a
simulated provider error preserves the draft, returns 503 plus X-Request-ID, logs
safe diagnostics and never logs the injected private values. Build, vet and the
full race suite passed against disposable fixtures. Only the local Notes image
was rebuilt/restarted to make these diagnostics available for the user's retry.

The retry identified a provider-side PutObject `501 NotImplemented`, surfaced by
Notes as 503. Custom endpoints now match rclone's optional AWS checksum settings,
with Content-MD5 integrity and conditional writes retained. TLS fixture tests
reject optional AWS CRC/trailer headers and verify exact bodies, lengths, MD5,
create/update preconditions and successful uploads. Error XML diagnostics capture
at most 8 KiB, classify only known unsupported header names, and discard unknown
or private values. These fixture checks do not establish actual Backblaze write
compatibility; another user save is needed to verify the provider response.
Build, vet, the regular suite and the full race suite pass with disposable test
backends. The local Notes image was rebuilt and its container restarted with this
change; the real provider was not written to by diagnostics.

The next user save confirmed that Backblaze rejects conditional-write headers:
PutObject returned `501 NotImplemented` with `s3_feature=conditional_write`.
Removing optional AWS checksum headers did not resolve the save failure. Notes
continues to refuse automatic fallback. The user then requested the explicit
connection switch and PostgreSQL optimistic-concurrency fallback described below.

## Conditional-write switch and database optimistic concurrency

Migration 00003 adds a conditional-writes setting defaulting to true and an
owner/notebook-scoped write-revision table, separate from the search index.
The connection form uses a Pico switch and explains the remaining external-writer
race. Existing connections retain strict mode until their owner switches it off.
Compatibility mode issues independent DB revision validators, uses transactional
compare-and-swap under the shared object lock, retains deletion tombstones, and
keeps current S3 HEAD checks and encryption.

New fixture tests cover the default UI switch and persisted edits; rejection of
implicit fallback; plain/encrypted compatibility CRUD against a provider rejecting
conditional writes; simultaneous same-content saves from separate handles with
one success and one conflict; stale saves/deletes; failed-upload rollback;
completed external changes; delete/recreate and policy-toggle invalidation;
index-only clearing preserving revisions; editor/API write tokens and preserved
drafts; attachment writes; and queue SQL failures not rolling back saved revisions.
No diagnostic writes were sent to Backblaze; actual provider saves still require
the user's connection switch to be disabled and their draft retried.

Completed on 2026-11-11: gofmt, `go build ./...`, `go vet ./...`,
`go test -timeout 180s ./...`, and `go test -race -timeout 180s ./...` pass with
disposable loopback PostgreSQL and in-memory S3. The local Notes image was rebuilt
and its container restarted successfully; readiness is healthy. Read-only runtime
checks confirmed the connection form exposes the switch checked by default and
the existing connection retained conditional writes enabled after migration.
No existing connection policy was changed and no real provider write was used
for validation. No image was published or Swarm deployment performed.

## Frontend mockup implementation

Completed on 2026-11-11. The embedded UI now uses the reviewed Notes shell,
sidebar, compact library rows, tag collections, reader, split editor and mobile
layouts with automatic light/dark palettes. Storage forms retain user-managed
connections, notebook mapping, encryption and conditional-write controls.

Validation completed:

- `gofmt` on changed Go files, `go build ./...`, `go vet ./...`,
  `go test -timeout 180s ./...`, and `go test -race -timeout 180s ./...`.
  Integration used a new disposable loopback PostgreSQL cluster and in-memory S3;
  tests used random schemas. Parent and Notes `git diff --check` passed.
- New regression tests cover safe Markdown rendering (including unsafe autolinks,
  raw HTML, remote images and escaped code), imported front-matter preservation,
  scoped reader/tags/preview, preview without persistence, title/tag/body retention
  on conflicts, read-only actions, and conditional attachment removal with both
  S3 and database write validators. Just-uploaded attachments appear before their
  asynchronous index job completes.
- Chromium checked eight screens across light/dark and 1440, 768, 390 and 320px:
  64 layouts, one primary heading, loaded branding and no horizontal overflow.
  Desktop/mobile screenshots were compared with the checked-in mockups.
- Browser interactions in both themes checked keyboard skip navigation,
  search/reset, tag navigation, reading, Markdown/attachment downloads, live
  preview, safe rendering, explicit save/create, conflict preservation, attachment
  uploads/removal and the guard against attachment navigation discarding a draft.
  Connection-policy checks covered blank credential fields, database-mode saves
  and attachment removal, and read-only controls. No JavaScript or CSP errors
  remained; Pico search/chevron/check icons use local assets.

The temporary browser harness was removed. No real provider content, existing
connection policy, live auth flow, container, image publication or deployment was
changed. Real provider and gateway acceptance remain outside this local check.
