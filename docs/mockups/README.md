# Notes design concept

## Connection bucket controls

Every connection has **Use connection**, including personal sample cards. New
connection forms include **Fixed bucket (optional)** and default path-style
addressing off. The shared Cloud storage sample fixes `cloud-bucket`; its mapping
preview shows a read-only bucket. Team archive and Personal storage keep the
bucket editable. Existing edit fixtures retain their saved addressing choice.

## Shared S3 connection concept

New standalone previews: [administrator](shared-connections-admin.html),
[regular user](shared-connections-member.html),
[dark administrator](dark/shared-connections-admin.html) and
[dark regular user](dark/shared-connections-member.html).
Production now implements the shared connection controls and path behavior.
These pages remain independent static review fixtures.

- Administrators see **Shared** when adding a connection, off by default.
  Enabling it reveals **Add user ID as a prefix**, also off by default.
  Shared status is disabled on edits and cannot be changed after creation.
- Shared connections are available to everyone and managed only by admins.
  Regular users see **Use connection** without shared edit or credential controls.
  Personal connection management stays available to its owner.
- **Use connection** previews a notebook's effective path. Prefix on produces
  `s3://<bucket>/<gateway-user-uuid>/<notebook-prefix>/`; prefix off lets users
  access the same objects if they map the same bucket and prefix. Notebook
  settings and indexes remain personal. The preview covers the connection step;
  encryption and other notebook options continue in existing setup.
- The shared-only prefix option locks while any user has a notebook attached.
  Unused shared connections can change it. No S3 objects are moved or deleted.
  Existing Notes location
  locks, blank saved credentials and conditional-write controls remain represented.
- Notes and Files keep independent connection records. Sharing applies to all
  users within each service; it does not synchronize service credentials.

Dialogs retain drafts on simulated errors, return focus on Cancel/Close/Escape,
and show fixed sample cards on save. Forms send no entered values and make no
API writes. Role restrictions here are visual fixtures, not live authorization.

Screenshots: [admin list](shared-connections-admin-desktop.png),
[user list](shared-connections-member-desktop.png),
[add dialog](shared-connection-add-desktop.png),
[edit dialog](shared-connection-edit-desktop.png),
[path preview](shared-connection-path-desktop.png) and
[mobile add](shared-connection-add-mobile.png). Matching images live in `dark/`.

Markdown documents, tags, and note-owned attachments, using the existing Razzie
Cloud service style: PicoCSS controls, HTMX fixtures, slate canvas, navy actions,
gold navigation, the unchanged cloud logo, and matching dark palettes.

## Open the preview

From the Notes repository:

```sh
python3 -m http.server 8767 --bind 127.0.0.1
```

Visit <http://127.0.0.1:8767/docs/mockups/index.html>. Alternatively serve the parent
checkout and visit `/notes/docs/mockups/index.html`. Use HTTP so HTMX can load
fixtures. Every asset is local to this repository; no backend or frontend build is
needed. Stop the server with Ctrl+C.

## Screens

| Screen | Light page | Dark page | Desktop screenshots |
| --- | --- | --- | --- |
| Note library | [index.html](index.html) | [dark/index.html](dark/index.html) | [Light](index-desktop.png) · [Dark](dark/index-desktop.png) |
| Read a note | [note.html](note.html) | [dark/note.html](dark/note.html) | [Light](note-desktop.png) · [Dark](dark/note-desktop.png) |
| Edit Markdown | [edit.html](edit.html) | [dark/edit.html](dark/edit.html) | [Light](edit-desktop.png) · [Dark](dark/edit-desktop.png) |
| Create a note | [new.html](new.html) | [dark/new.html](dark/new.html) | [Light](new-desktop.png) · [Dark](dark/new-desktop.png) |
| Tag collections | [tags.html](tags.html) | [dark/tags.html](dark/tags.html) | [Light](tags-desktop.png) · [Dark](dark/tags-desktop.png) |
| Notes tagged homelab | [tag.html](tag.html) | [dark/tag.html](dark/tag.html) | [Light](tag-desktop.png) · [Dark](dark/tag-desktop.png) |
| Empty notebook | [empty.html](empty.html) | [dark/empty.html](dark/empty.html) | [Light](empty-desktop.png) · [Dark](dark/empty-desktop.png) |
| Connections | [storage.html](storage.html) | [dark/storage.html](dark/storage.html) | [Light](storage-desktop.png) · [Dark](dark/storage-desktop.png) |
| First connection | [storage-empty.html](storage-empty.html) | [dark/storage-empty.html](dark/storage-empty.html) | [Light](storage-empty-desktop.png) · [Dark](dark/storage-empty-desktop.png) |
| Notebooks | [notebooks.html](notebooks.html) | [dark/notebooks.html](dark/notebooks.html) | [Light](notebooks-desktop.png) · [Dark](dark/notebooks-desktop.png) |
| First notebook | [notebooks-empty.html](notebooks-empty.html) | [dark/notebooks-empty.html](dark/notebooks-empty.html) | [Light](notebooks-empty-desktop.png) · [Dark](dark/notebooks-empty-desktop.png) |

Mobile examples: [library](index-mobile.png), [editor](edit-mobile.png),
[dark library](dark/index-mobile.png), [dark editor](dark/edit-mobile.png).

## Try the interactions

- **Try sample search** shows the two example results for “storage”; **Reset**
  restores the library. The search term is fixed for this static concept.
- Note links open the same sample document. Tag links open the sample `homelab`
  collection, including cards for other labels. These are layout/navigation examples.
- **Edit note** pairs editable Markdown with a **fixed** rendered example. The
  preview does not re-render text as you type. Title and comma-separated tags are
  editable; they are not persisted. **New note** starts with blank fields.
- **Save changes** and **Create note** show explicit simulation feedback, keeping
  the form in place. No note is saved or added to the library.
- The attachment chooser keeps file contents on the device. The upload fixture
  shows feedback; **Remove** removes a sample row until reload. Reader downloads
  contain harmless Markdown and CSV samples, not remote storage objects.
- **Review save failure states** demonstrates an unavailable store and a stale
  revision while retaining the draft. Comparing revisions and retry logic belong
  to a future backend; no actual concurrency or storage error is generated.
- Storage settings show connection cards and **Add connection**. Add and Edit
  fetch forms with HTMX into an HTML5 `dialog`, opened with `showModal()`.
  Edit pre-fills the selected connection's settings and leaves credentials blank.
  Location fields are disabled on the sample connection with an attached notebook.
  The first connection form stays inline in the empty state. Cancel, Close and
  Escape discard the draft and return focus. Save closes the dialog and displays
  fixture feedback with fixed sample cards; entered values are not persisted.
- **Preview a save error** keeps the connection draft in place. Never enter real
  credentials. Connection forms use `hx-params="none"` and unnamed fields; no
  credential values are transmitted.
- **Notebooks** has its own sidebar page, separate from **Connections**. Existing
  notebooks show cards with connection, bucket, prefix, effective access and
  encryption. **Add notebook** opens a dialog; with no notebooks, the first form
  stays inline. The empty fixture assumes connections already exist.
- **Edit notebook** opens a prefilled dialog. Name and read-only policy are
  editable; location and encryption remain fixed, matching the live service's
  rules. Connection-level read-only access takes precedence. The add form includes
  optional rclone crypt settings, with advanced options in a disclosure.
- Notebook Cancel, Close and Escape discard the dialog draft and restore focus.
  A simulated error retains the draft, including sample crypt passwords. Save
  returns authored sample cards and feedback; it does not use or persist entered
  values. Forms have unnamed inputs and `hx-params="none"`, including passwords.
  **Preview removal** only displays feedback; all sample cards remain.
- The preview banner switches themes on the same screen and opens the empty state.
  Theme switching or navigation resets unsaved mockup inputs.

## Boundaries and assets

Notes now has an independent Go service. See [its architecture](../architecture.md)
for owner-managed connections, separate notebook mappings, optional rclone crypt,
S3 authority and derived PostgreSQL indexing. These mockups remain static sample
artifacts, separate from live settings and content. Storage settings reflect the
current connection fields, including conditional writes. Notebook forms show the
current crypt controls without performing storage or password verification.
Removal shows feedback without deleting sample cards. Other screens remain
broader design references.

`assets/` contains exact copies of Files' vendored PicoCSS, HTMX, their licenses,
and the brand logo. `mockup.css` and `dark.css` originate from Files' design
mockups; `notes.css` adds Notes layouts. Keep the light and dark HTML in sync.
`storage-connections.js` provides the connection dialog lifecycle; `notebooks.js`
handles notebook dialogs and encryption field visibility. HTMX fetches authored
fixtures. There are no external asset requests or package installations.
CSP disallows inline scripts/styles and HTMX evaluation/history snapshots are off.

Storage dialog screenshots: [desktop](storage-edit-desktop.png),
[mobile](storage-edit-mobile.png), [dark desktop](dark/storage-edit-desktop.png),
[dark mobile](dark/storage-edit-mobile.png). Existing/empty storage screenshots
are linked in the screen table above. The reviewed connection flow is now
implemented in the live templates with real saves, revision checks and error
feedback that retains the browser draft. These fixtures still simulate mutations.

Notebook dialog screenshots: [edit desktop](notebooks-edit-desktop.png),
[edit mobile](notebooks-edit-mobile.png), [dark edit desktop](dark/notebooks-edit-desktop.png),
[dark edit mobile](dark/notebooks-edit-mobile.png),
[add desktop](notebooks-add-desktop.png), [add mobile](notebooks-add-mobile.png),
[dark add desktop](dark/notebooks-add-desktop.png), [dark add mobile](dark/notebooks-add-mobile.png),
[add with encryption](notebooks-add-encrypted-desktop.png), and
[dark add with encryption](dark/notebooks-add-encrypted-desktop.png).

## Verification

The notebook-page revision passed Chromium checks of all 22 full pages in light
and dark at 320, 390, 768 and 1440 pixels: 88 layout cases. Checks covered one h1,
loaded assets, sidebar links, and no page-level horizontal overflow. Sixteen
notebook empty/populated interaction cases exercised native validation, dialog
focus and focus return, Cancel/Close/Escape, fresh forms, immutable edit settings,
connection read-only guidance, crypt visibility/validation, retained error drafts,
and static saves. Edit/removal, connection-dialog regression and theme/navigation
round trips also passed. Across 1,536 requests, all used GET and none contained
entered sentinel settings/passwords; there were no console/page errors or failed
requests. Local links, unique IDs, matching theme fixtures and Git whitespace
checks passed. Updated desktop/mobile screenshots were inspected visually.
Only mockups and documentation changed; Go checks and live backends were not run.

The revised storage concept passed Chromium checks in both themes at 320, 390,
768 and 1440 pixels for existing/empty states (16 layout cases). Checks covered
Add/Edit, native modal focus, Escape/Close/Cancel, prefill, blank credentials,
locked location fields, validation, error draft retention, simulated saves,
removal/notebook feedback and theme round trips. No entered values were
transmitted and no browser errors or failed responses were observed. Local
references, unique IDs, synchronized modal assets and Git whitespace were checked;
storage screenshots were inspected visually. The following records the earlier
full-suite mockup pass.

Headless Chromium through Playwright checked all 16 full pages at 1440×1050,
768×1024, 390×844, and 320×800: 64 layout cases with one primary heading,
loaded brand assets, the expected theme, and no page-level horizontal overflow.
Twenty screenshots cover all desktop pages and the library/editor on mobile.

Both themes exercised search/reset, reading and edit navigation, both attachment
downloads, save/create fixtures, attachment selection/upload/removal, storage setup,
and theme switching. Save failure and stale-revision fixtures retained edited text.
The keyboard skip link was checked. No browser console errors, page errors, or
failed HTTP responses were observed. Local HTML/fixture references were checked,
and the logo was compared with the parent master asset. Git whitespace checks ran.

No Go checks were needed for these static artifacts. Live authentication,
PostgreSQL, S3, encryption, concurrency and deployment were not exercised.
