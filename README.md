# Notes

Private Markdown notes, tags, search and attachments for Razzie Cloud. Go service
with embedded PicoCSS/HTMX, gateway SSO, S3 storage, optional rclone crypt, and River
background indexing. See [architecture and API](docs/architecture.md) for contracts.

Storage setup follows **Files**: sign in, add an S3 connection, then attach a named
notebook to a bucket/prefix with optional encryption. Notes owns its own settings;
it does not reuse Files' saved credentials or directory records. No S3 credentials
or bucket are required in service environment variables or Swarm secrets.

## Set up storage

1. Open **Storage → Add connection**. Choose AWS, Backblaze or another compatible
   provider; enter endpoint, region, path-style addressing, access/secret keys and
   an optional session token. A connection can be read-only. **Conditional writes**
   defaults on; turn it off for providers such as Backblaze that reject these
   requests. Notes then uses PostgreSQL optimistic revision checks for its own
   edits. Changes made by external clients during a save can still be overwritten.
2. Under **Attach a notebook**, choose the connection, bucket and a dedicated
   prefix. The notebook can also be read-only; connection policy takes precedence.
3. To encrypt, enable **Encrypt with rclone crypt** and enter the crypt password
   and optional salt/second password. Set filename encryption, directory-name
   encryption, encoding and suffix to match your rclone configuration.
4. For a new encrypted notebook, confirm an **empty prefix**. Attaching existing
   encrypted data requires a known nonempty decrypted file path, such as
   `notes/example.md`, to verify the password before the mapping is saved.
5. Download **crypt recovery** and keep it securely. Removing a mapping leaves S3
   objects and recovery records intact. Removing a connection requires detaching
   its notebooks first.

Connection names, credentials, read-only policy and conditional writes can be updated. Leave secret
fields blank to keep their values; clearing a session token has a separate switch.
Connection location fields are fixed while notebooks are attached. A notebook's
bucket/prefix and crypt options are immutable: attach a new mapping to change them.
A notebook removal is configuration cleanup and never deletes bucket contents.

Each notebook stores Markdown at `<prefix>/notes/<path>.md` and attachments at
`<prefix>/attachments/<path>.md/<filename>`. Encryption transparently transforms
these names/content through rclone crypt. The UI uses explicit save and opaque
write validators, preserves conflicting drafts and warns about unsaved changes.
The mockup-based sidebar, note library, tag collections and reader support both
automatic light/dark themes and mobile layouts. The split editor has title/tag
fields, live Markdown preview, and attachment downloads, uploads and removal.
Title/tag edits retain imported IDs, timestamps and other YAML front matter.
Preview omits raw HTML and images; it never saves a draft. Search and attachment
lists update in the background.

## Run locally

In the parent checkout, set local passwords in `.env`, including `S3_DEV_PASSWORD`:

```sh
docker compose --profile storage-demo up --build -d
```

Open `http://notes.localhost:8080`, then add a connection in **Storage**:

- Provider: Other; endpoint: `http://s3-demo:9000`; region: `us-east-1`.
- Path-style addressing: on; access key: `files-dev`; secret: `S3_DEV_PASSWORD`.
- Notebook bucket: `files-demo`; prefix: a new dedicated path, e.g. `my-notes`.

This demo bucket is **in-memory** and disappears when its fixture restarts. For
durable storage, enter your actual provider credentials in the UI instead. Private
HTTP endpoints require operator-managed egress exceptions, as they do in Files.

To run the service binary behind a trusted gateway/proxy, set `POSTGRES_URI` or
`POSTGRES_URI_FROM`, set its public `ORIGIN`, then `go run ./cmd/notes`. Startup works
with no storage connections; the first-use page guides you through setup. Do not
expose the service port directly: identity comes from trusted gateway UUID headers.

## Configuration

Environment variables have matching kebab-case CLI flags; flags override the
environment. The PostgreSQL password file overrides the URI password.

| Environment | Default / meaning |
| --- | --- |
| `SERVICE_PORT`, `ORIGIN` | `8080`, `http://localhost:8080`; exact public origin |
| `POSTGRES_URI`, `POSTGRES_URI_FROM` | Exactly one; discovery path `/v1/instances/postgres/notes/uri` |
| `POSTGRES_PASSWORD_FILE` | Optional password override |
| `S3_ALLOWED_PRIVATE_ENDPOINTS`, `S3_ALLOW_HTTP` | Empty, `false`; exact host:port exceptions; HTTP needs both |
| `SCAN_INTERVAL`, `INDEX_WORKERS` | `5m`, `4` indexing workers per replica; workers 1–32 |
| `MAX_NOTE_BYTES`, `MAX_ATTACHMENT_BYTES` | 2 MiB, 32 MiB; bounded transfers |
| `LOG_LEVEL` | `INFO`; also `DEBUG`, `WARN`, `ERROR` |

For Swarm, publish the Notes image and select `NOTES_IMAGE_TAG`. Two replicas share
PostgreSQL settings and jobs; no Notes-specific volume, master key, S3 environment
credentials or Docker provider secrets are needed. Rebuild/publish the landing
image to expose its Notes link. Deployment remains a separate operator action.

## Backup and rebuild

S3 is authoritative for note bodies, front matter and attachments. PostgreSQL has
**durable settings and secrets** alongside **disposable index/scan/River tables**.
Back up PostgreSQL as with Files: it contains connection credentials, bucket/prefix
mappings and crypt passwords. These are not reconstructed by scanning S3.
Encryption protects objects at the provider; Notes decrypts them on the server,
and the search index contains plaintext text. Protect database backups accordingly.

To rebuild, keep settings and enqueue reconciliation through the admin API. After
full database loss, restore a backup or re-enter connections, mappings and exact
crypt settings, then S3 scans reconstruct search. Preserve gateway identities too.
Without a backup or recovery passwords, encrypted content cannot be recovered.
Attaching an existing notebook schedules a scan automatically.

Upgrading the earlier operator-bucket prototype clears its **derived index only**.
Its old S3 objects remain untouched. Re-enter credentials through Storage and attach
its `<old-prefix>/<gateway-user-uuid>` prefix to recover the previous `notes/` and
`attachments/` layout. The obsolete deployment-wide S3 flags/secrets are removed.

## Verification

```sh
gofmt -w cmd internal
go build ./...
go vet ./...
go test ./...
TEST_DATABASE_URL='postgres://user@127.0.0.1:5432/disposable?sslmode=disable' go test -race ./...
```

Integration tests create/drop random schemas on disposable **loopback-only**
PostgreSQL; they skip without `TEST_DATABASE_URL`. S3 tests start in-memory servers
with conditional-write enforcement. The suite also checks compatibility with an
independent rclone filesystem. With conditional writes off, database optimistic
concurrency protects Notes edits, but cannot make external S3 writes atomic with
its checks. See the [verification record](docs/verification.md).
GitHub Actions run integration/race checks and publish tag-named GHCR images.

Static [design mockups](docs/mockups/README.md) remain separate read-only fixtures.

The header logo and Razzie Cloud name link to `LANDING_ORIGIN`
(`--landing-origin`, default `https://razzie.cloud`). Set this to the exact
HTTP(S) origin of your landing page, without a trailing slash. The adjacent
service name opens this app. Compose and Swarm set the landing origin for their
local/public domain.

Set `ACCESS_GATEWAY_URL` (`--access-gateway-url`) to the gateway's exact origin,
for example `http://access-gateway:8080` on the private container network, to
require signed proxy identity. Startup fetches `/.well-known/paseto-keys` and
fails if discovery fails. Expired key sets refresh on use; stale keys are never
accepted. Caddy must strip incoming `X-Auth-Request-Token` and copy the gateway's
value alongside UUID/name. The service verifies the PASETO signature, issuer,
public app `ORIGIN`, expiry, method, URI and matching identity headers.
An empty setting retains trusted-proxy header mode. See the gateway's
[signed identity contract](https://github.com/razzie-cloud/access-gateway/blob/master/docs/reference.md#signed-proxy-identity).
