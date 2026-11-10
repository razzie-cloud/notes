# Notes development guide

- This is an independent Git repository, included as the parent platform's
  `notes/` submodule. Keep service work here and platform coordination in the parent.
- Current scope is static mockups and a provisional architecture. Do not describe
  storage, authentication, saving, or deployment as implemented.
- Follow the established Razzie Cloud PicoCSS/HTMX visual style. Assets are local
  to `docs/mockups/assets/`, including their licenses and the unchanged brand logo.
- Keep light pages and their `dark/` counterparts aligned. Forms only request
  local static fixtures. Never introduce real service writes into these previews.
- Preserve Markdown as portable content, with tags and note-owned attachments.
  S3 through embedded rclone is a proposal, not an approved backend commitment.
- Before implementing storage, read `docs/architecture.md` and the current Files
  storage/egress code. Do not copy Files source or invent custom encryption.
- Future metadata and storage operations must be scoped to the gateway user UUID;
  tags, attachments, and note IDs cannot grant access on their own.
- For mockup changes, serve over loopback HTTP and check local links/assets,
  HTMX responses, keyboard controls, both themes, and desktop/mobile overflow.
  Report checks and limitations. Go checks apply once a Go service exists.
- Do not add Notes to live Compose/Swarm routing or deploy it as part of design work.
