# Notes design concept

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
| Storage settings | [storage.html](storage.html) | [dark/storage.html](dark/storage.html) | [Light](storage-desktop.png) · [Dark](dark/storage-desktop.png) |
| First connection | [storage-empty.html](storage-empty.html) | [dark/storage-empty.html](dark/storage-empty.html) | [Light](storage-empty-desktop.png) · [Dark](dark/storage-empty-desktop.png) |

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
  credential values are transmitted. Notebook attachment is a separate compact
  plain-S3 fixture and does not create a notebook.
- The preview banner switches themes on the same screen and opens the empty state.
  Theme switching or navigation resets unsaved mockup inputs.

## Boundaries and assets

Notes now has an independent Go service. See [its architecture](../architecture.md)
for owner-managed connections, separate notebook mappings, optional rclone crypt,
S3 authority and derived PostgreSQL indexing. These mockups remain static sample
artifacts, separate from live settings and content. Storage settings reflect the
current connection fields, including conditional writes; advanced notebook crypt
setup is outside this concept. Removal shows feedback without deleting sample
cards. Other screens remain broader design references.

`assets/` contains exact copies of Files' vendored PicoCSS, HTMX, their licenses,
and the brand logo. `mockup.css` and `dark.css` originate from Files' design
mockups; `notes.css` adds Notes layouts. Keep the light and dark HTML in sync.
`storage-connections.js` provides the native dialog lifecycle; HTMX fetches authored
fixtures. There are no external asset requests or package installations.
CSP disallows inline scripts/styles and HTMX evaluation/history snapshots are off.

Storage dialog screenshots: [desktop](storage-edit-desktop.png),
[mobile](storage-edit-mobile.png), [dark desktop](dark/storage-edit-desktop.png),
[dark mobile](dark/storage-edit-mobile.png). Existing/empty storage screenshots
are linked in the screen table above. The reviewed connection flow is now
implemented in the live templates with real saves, revision checks and error
feedback that retains the browser draft. These fixtures still simulate mutations.

## Verification

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
