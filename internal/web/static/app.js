"use strict";
const editor = document.querySelector("#editor");
if (editor) {
  let dirty = editor.dataset.failed === "true";
  const draftStatus = document.querySelector("#draft-status");
  const preview = document.querySelector("#markdown-preview");
  const previewStatus = document.querySelector("#preview-status");
  const content = editor.querySelector("[name=content]");
  let timer;
  let pending;
  editor.addEventListener("input", () => {
    dirty = true;
    draftStatus.textContent = "Unsaved changes";
  });
  editor.addEventListener("submit", () => { dirty = false; });
  window.addEventListener("beforeunload", (event) => {
    if (dirty) { event.preventDefault(); event.returnValue = ""; }
  });
  // Attachment navigation must never discard an unsaved Markdown draft.
  for (const form of document.querySelectorAll("[data-attachment-form]")) {
    form.addEventListener("submit", (event) => {
      if (dirty) {
        event.preventDefault();
        document.querySelector(".attachment-status").hidden = false;
        draftStatus.textContent = "Save changes before changing attachments";
      }
    });
  }
  content.addEventListener("input", () => {
    clearTimeout(timer);
    if (pending) pending.abort();
    previewStatus.textContent = "Updating…";
    timer = setTimeout(async () => {
      pending = new AbortController();
      try {
        const response = await fetch(preview.dataset.url, {
          method: "POST", signal: pending.signal,
          body: new URLSearchParams({ content: content.value, key: editor.elements.key.value })
        });
        if (!response.ok) throw new Error("Preview unavailable");
        preview.innerHTML = await response.text();
        previewStatus.textContent = "Live preview";
      } catch (error) {
        if (error.name !== "AbortError") previewStatus.textContent = "Preview unavailable · draft kept";
      }
    }, 350);
  });
}

function configureEncryption() {
  const encryption = document.querySelector("#encrypted");
  if (!encryption || encryption.dataset.bound) return;
  encryption.dataset.bound = "true";
  const options = document.querySelector("#crypt-options");
  const password = options.querySelector("[name=crypt_password]");
  const initialize = options.querySelector("[name=new_crypt_prefix]");
  const verify = options.querySelector("[name=verify_path]");
  const update = () => {
    for (const control of options.querySelectorAll("input, select")) control.disabled = !encryption.checked;
    password.required = encryption.checked;
    verify.required = encryption.checked && !initialize.checked;
    if (encryption.checked) options.open = true;
  };
  encryption.addEventListener("change", update);
  initialize.addEventListener("change", update);
  update();
}

configureEncryption();
document.addEventListener("htmx:afterSwap", configureEncryption);
