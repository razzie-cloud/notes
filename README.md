# Notes

Markdown documents with tags and attachments for Razzie Cloud.

This repository currently contains **design mockups and an architecture proposal**.
There is no service binary, database schema, storage integration, or deployment yet.

## Review the mockups

From this repository, run:

```sh
python3 -m http.server 8767 --bind 127.0.0.1
```

Open <http://127.0.0.1:8767/docs/mockups/index.html>. The mockups are self-contained;
they also work when served from the Razzie Cloud parent checkout. No cloud account,
credentials, package installation, or frontend build is needed.

The [screen guide](docs/mockups/README.md) covers the library, reading, Markdown
editing, new notes, tags, empty state, and storage setup in light and dark themes.
Forms use static HTMX fixtures; edits, tags, and selected attachments are not saved.
Downloads contain harmless example files.

## Proposed service

The likely storage direction is embedded rclone with S3, following Files. That
choice is provisional. The [architecture proposal](docs/architecture.md) records
the content model, ownership boundaries, storage layout, and unresolved decisions.
The mockups do not imply that Notes shares Files credentials or directory records.

Future implementation should use Go with embedded HTMX/PicoCSS assets and the
existing gateway identity. Notes owns its service code and deployment artifacts;
platform integration belongs in the parent repository and infra submodule.
