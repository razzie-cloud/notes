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
| Proposed storage setup | [storage.html](storage.html) | [dark/storage.html](dark/storage.html) | [Light](storage-desktop.png) · [Dark](dark/storage-desktop.png) |

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
- **Try connection setup** shows a fixture response without contacting S3. Never
  enter real credentials. Credential fields have no submitted names, and fixture
  requests do not upload selected file contents.
- The preview banner switches themes on the same screen and opens the empty state.
  Theme switching or navigation resets unsaved mockup inputs.

## Boundaries and assets

This repository has no Go service, database schema, authentication, or deployment.
The S3/rclone direction is provisional; see [the architecture proposal](../architecture.md).
Read-only connections, deletion, sharing, history, and advanced encryption settings
are not modeled here. A future service needs sanitized Markdown and owner-checked
attachment routes; this concept renders only authored static sample HTML.

`assets/` contains exact copies of Files' vendored PicoCSS, HTMX, their licenses,
and the brand logo. `mockup.css` and `dark.css` originate from Files' design
mockups; `notes.css` adds Notes layouts. Keep the light and dark HTML in sync.
There is no custom JavaScript, external asset request, or package installation.
CSP disallows inline scripts/styles and HTMX evaluation/history snapshots are off.

## Verification

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

No Go checks or live authentication, PostgreSQL, S3, encryption, concurrency, or
deployment checks apply to these static artifacts. Nothing was deployed.
