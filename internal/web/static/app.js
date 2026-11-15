"use strict";
// Navigation swaps the notebook context and sidebar together. Keep one set of
// browser listeners while editor instances, previews and requests come and go.
(() => {
  let state;
  const navigationConsent = new WeakMap();
  let lastPosition = -1;
  try { lastPosition = Number(sessionStorage.getItem("notes-history-position") ?? -1); } catch {}
  // Native save/upload redirects create a new document and an entry with no
  // state. Continue the tab's position sequence so cancellation can still find
  // the entry on the correct side of earlier HTMX pages.
  let position = Number.isInteger(history.state?.notesPosition) ? history.state.notesPosition : lastPosition + 1;
  let currentURL = location.href;
  let rendered = { position, url: currentURL };
  let returning = false;
  let historyConsent;
  let pendingHistory;
  let generation = 0;
  function rememberPosition() {
    history.replaceState({ ...history.state, notesPosition: position }, "", location.href);
    try { sessionStorage.setItem("notes-history-position", String(position)); } catch {}
  }
  rememberPosition();
  function discardDraft() {
    return !state?.dirty || window.confirm("Discard your unsaved note changes?");
  }
  function revision() { return state?.dirty ? state.revision : null; }
  function stopPreview() {
    if (!state) return;
    clearTimeout(state.timer);
    state.pending?.abort();
  }
  function initialize() {
    const editor = document.getElementById("editor");
    // A native explicit save renders its result at the POST URL. Give that
    // editor a reloadable GET URL before it becomes an HTMX history entry.
    if (editor && location.pathname === "/save") {
      const url = new URL("/edit", location.href);
      url.search = new URLSearchParams({ notebook: document.getElementById("main").dataset.notebook, key: editor.elements.key.value });
      history.replaceState(history.state, "", url);
      currentURL = location.href;
      rendered = { position, url: currentURL };
    }
    if (state?.editor !== editor) {
      stopPreview();
      state = editor ? { editor, dirty: editor.dataset.failed === "true", revision: 0 } : null;
    }
    const notebook = document.getElementById("main")?.dataset.notebook;
    const home = document.querySelector(".service-name");
    if (home && notebook !== undefined) home.href = "/?notebook=" + encodeURIComponent(notebook);
  }
  function navigationError(message, destination = location.href) {
    if (!returning && location.href !== rendered.url) restorePosition(rendered);
    const status = document.getElementById("navigation-status");
    if (!status) return;
    status.hidden = false;
    status.textContent = message + " ";
    const reload = document.createElement("a");
    const url = new URL(destination, location.href);
    reload.href = url.origin === location.origin ? url.href : location.href;
    reload.textContent = "Reload page";
    status.append(reload);
    status.scrollIntoView({ block: "nearest" });
  }
  document.addEventListener("input", event => {
    if (!state || !state.editor.contains(event.target)) return;
    state.dirty = true;
    state.revision++;
    document.getElementById("draft-status").textContent = "Unsaved changes";
    if (event.target.name !== "content") return;
    stopPreview();
    const active = state;
    const preview = document.getElementById("markdown-preview");
    const status = document.getElementById("preview-status");
    status.textContent = "Updating…";
    active.timer = setTimeout(async () => {
      const pending = new AbortController();
      active.pending = pending;
      try {
        const response = await fetch(preview.dataset.url, {
          method: "POST", signal: pending.signal,
          body: new URLSearchParams({ content: active.editor.elements.content.value, key: active.editor.elements.key.value })
        });
        if (!response.ok) throw new Error("Preview unavailable");
        const html = await response.text();
        if (state === active && active.pending === pending) {
          preview.innerHTML = html;
          htmx.process(preview);
          status.textContent = "Live preview";
        }
      } catch (error) {
        if (error.name !== "AbortError" && state === active && active.pending === pending) status.textContent = "Preview unavailable · draft kept";
      }
    }, 350);
  });
  document.addEventListener("submit", event => {
    if (event.target === state?.editor) state.dirty = false;
    // Attachment changes retain their normal POST/redirect flow. Do not submit
    // them while an unsaved Markdown draft would be discarded by the response.
    if (state?.dirty && event.target.matches("[data-attachment-form]")) {
      event.preventDefault();
      document.querySelector(".attachment-status").hidden = false;
      document.getElementById("draft-status").textContent = "Save changes before changing attachments";
    }
  });
  window.addEventListener("beforeunload", event => {
    if (state?.dirty) { event.preventDefault(); event.returnValue = ""; }
  });
  document.addEventListener("htmx:beforeRequest", event => {
    if (event.detail.target?.id !== "app-layout") return;
    if (!discardDraft()) { event.preventDefault(); return; }
    pendingHistory?.abort();
    pendingHistory = null;
    navigationConsent.set(event.detail.xhr, { revision: revision(), generation: ++generation });
  });
  document.addEventListener("htmx:beforeSwap", event => {
    if (event.detail.target?.id !== "app-layout") return;
    const consent = navigationConsent.get(event.detail.xhr);
    if (consent && consent.generation !== generation) {
      event.detail.shouldSwap = false;
      return;
    }
    if (event.detail.xhr.status >= 400) {
      event.detail.shouldSwap = false;
      event.detail.isError = false;
      navigationError(event.detail.xhr.status === 401
        ? "Your session could not be verified. Your current page and any draft are still here."
        : "This page could not be opened. Your current page and any draft are still here.", event.detail.xhr.responseURL);
      return;
    }
    // A draft can change while the navigation request is in flight.
    if (event.detail.shouldSwap && revision() !== consent?.revision && !discardDraft()) {
      event.detail.shouldSwap = false;
      if (location.href !== rendered.url) restorePosition(rendered);
    }
  });
  document.addEventListener("htmx:beforeCleanupElement", event => {
    if (state && (event.detail.elt === state.editor || event.detail.elt.contains(state.editor))) {
      stopPreview();
      state = null;
    }
  });
  document.addEventListener("htmx:afterSwap", event => {
    initialize();
    if (event.target.id === "app-layout") {
      // Bind links before the settle phase so a rapid second click remains an
      // HTMX navigation rather than following an unprocessed native anchor.
      htmx.process(document.getElementById("app-layout"));
      rendered = { position, url: location.href };
    }
  });
  document.addEventListener("htmx:afterSettle", event => {
    if (event.target.id !== "app-layout") return;
    document.getElementById("main")?.focus({ preventScroll: true });
  });
  document.addEventListener("htmx:sendError", event => {
    if (event.detail.target?.id !== "app-layout") return;
    navigationError("Navigation failed or sign-in is required. Your current page and any draft are still here.", event.detail.pathInfo?.finalRequestPath);
  });
  document.addEventListener("htmx:timeout", event => {
    if (event.detail.target?.id === "app-layout") navigationError("Navigation timed out. Your current page and any draft are still here.");
  });
  document.addEventListener("htmx:pushedIntoHistory", () => {
    position++;
    currentURL = location.href;
    rememberPosition();
  });
  document.addEventListener("htmx:replacedInHistory", () => {
    currentURL = location.href;
    rememberPosition();
  });
  // HTMX marks the outgoing entry immediately before this event, replacing its
  // state object. Reattach our position before HTMX pushes the next entry.
  document.addEventListener("htmx:beforeHistoryUpdate", rememberPosition);
  function restorePosition(previous) {
    const delta = previous.position - position;
    position = previous.position;
    currentURL = previous.url;
    if (delta) {
      returning = true;
      history.go(delta);
    } else {
      history.replaceState({ ...history.state, notesPosition: position }, "", currentURL);
    }
  }
  // This capture listener runs before HTMX's popstate handler. Canceling Back
  // or Forward must restore the original entry as well as keep the live draft.
  window.addEventListener("popstate", event => {
    if (returning) { returning = false; rememberPosition(); event.stopImmediatePropagation(); return; }
    if (!event.state?.htmx) return;
    pendingHistory?.abort();
    pendingHistory = null;
    generation++;
    htmx.trigger(document.getElementById("app-layout"), "htmx:abort");
    const previous = rendered;
    position = Number.isInteger(event.state.notesPosition) ? event.state.notesPosition : position;
    if (!discardDraft()) {
      event.stopImmediatePropagation();
      restorePosition(previous);
      return;
    }
    currentURL = location.href;
    historyConsent = { previous, revision: revision() };
  }, true);
  // History snapshots stay disabled. HTMX fetches fresh HTML and restores only
  // hx-history-elt, keeping header/dialog controllers and their listeners alive.
  document.addEventListener("htmx:historyCacheMiss", event => {
    const consent = historyConsent;
    const xhr = event.detail.xhr;
    pendingHistory = xhr;
    rememberPosition();
    xhr.addEventListener("load", load => {
      if (pendingHistory !== xhr) {
        load.stopImmediatePropagation();
        return;
      }
      pendingHistory = null;
      if (xhr.status >= 400) {
        load.stopImmediatePropagation();
        if (consent) restorePosition(consent.previous);
        navigationError("This page could not be restored. Your current page and any draft are still here.");
      } else if (consent && revision() !== consent.revision && !discardDraft()) {
        load.stopImmediatePropagation();
        restorePosition(consent.previous);
      }
    }, true);
    xhr.addEventListener("error", () => {
      if (pendingHistory !== xhr) return;
      pendingHistory = null;
      if (consent) restorePosition(consent.previous);
      navigationError("Navigation failed or sign-in is required. Your current page and any draft are still here.");
    });
  });
  document.addEventListener("htmx:historyRestore", () => {
    initialize();
    currentURL = location.href;
    rememberPosition();
    document.getElementById("main")?.focus({ preventScroll: true });
  });
  initialize();
})();

function configureEncryption() {
  const encryption = document.querySelector("#encrypted");
  if (!encryption || encryption.dataset.bound) return;
  encryption.dataset.bound = "true";
  const options = document.querySelector("#crypt-options");
  const password = options.querySelector("[name=crypt_password]");
  const initialize = options.querySelector("[name=new_crypt_prefix]");
  const verify = options.querySelector("[name=verify_path]");
  const update = () => {
    options.hidden = !encryption.checked;
    encryption.setAttribute("aria-expanded", String(encryption.checked));
    for (const control of options.querySelectorAll("input, select")) control.disabled = !encryption.checked;
    password.required = encryption.checked;
    verify.required = encryption.checked && !initialize.checked;
    verify.disabled = !encryption.checked || initialize.checked;
    document.getElementById("crypt-existing-file").hidden = initialize.checked;
  };
  encryption.addEventListener("change", update);
  initialize.addEventListener("change", update);
  update();
}

configureEncryption();
document.addEventListener("htmx:afterSwap", configureEncryption);
