"use strict";
// The dialog lives outside replaceable page content. HTMX owns form loading and
// saves; native dialog APIs own modal focus and Escape behavior.
(() => {
  const dialog = document.getElementById("notebook-dialog");
  if (!dialog) return;
  const content = document.getElementById("notebook-dialog-content");
  let opener;
  function configureConnectionPolicy() {
    const connection = document.getElementById("notebook-connection");
    if (!connection) return;
    const help = document.getElementById("notebook-access-help");
    help.textContent = connection.selectedOptions[0]?.dataset.readOnly === "true"
      ? "This connection is read-only. The notebook stays read-only even when this switch is off."
      : "A read-only connection always takes precedence.";
  }
  document.addEventListener("change", event => {
    if (event.target.id === "notebook-connection") configureConnectionPolicy();
  });
  configureConnectionPolicy();
  function closeDialog() {
    dialog.close();
    // Clear drafts now, rather than in the queued native close event, which can
    // arrive after a subsequent Add/Edit request has loaded its form.
    content.replaceChildren();
    if (opener?.isConnected) opener.focus();
    else document.getElementById("add-notebook")?.focus();
  }
  document.addEventListener("htmx:beforeRequest", event => {
    if (event.detail.target === content) opener = event.detail.elt;
  });
  document.addEventListener("htmx:beforeSwap", event => {
    if (event.detail.xhr.status >= 400 && ["notebook-feedback", "notebook-list-feedback"].includes(event.detail.target.id)) {
      event.detail.shouldSwap = true;
      event.detail.isError = false;
    }
  });
  document.addEventListener("htmx:afterSwap", event => {
    configureConnectionPolicy();
    const target = event.detail.target || event.target;
    if (target === content && content.querySelector("[data-notebook-form]") && !dialog.open) {
      dialog.showModal();
      // Avoid autofocus attributes: HTMX's later settle phase would refocus the
      // name field while the user is already entering other settings.
      content.querySelector("[name=name]").focus();
    }
    if (["main", "app-layout"].includes(target.id)) {
      if (dialog.open) closeDialog();
      document.getElementById("add-notebook")?.focus();
    }
    if (["notebook-feedback", "notebook-list-feedback"].includes(target.id)) {
      target.focus();
      target.scrollIntoView({ block: "nearest" });
    }
  });
  document.addEventListener("click", event => {
    if (event.target.closest("[data-close-notebook]")) closeDialog();
  });
  dialog.addEventListener("cancel", event => {
    event.preventDefault();
    closeDialog();
  });
  document.addEventListener("htmx:sendError", event => {
    const form = event.detail.elt.closest("[data-notebook-form]");
    if (!form) return;
    const feedback = form.querySelector("#notebook-feedback");
    feedback.textContent = "Notebook could not be saved. Your entries are still here; check your connection and retry.";
    feedback.focus();
    feedback.scrollIntoView({ block: "nearest" });
  });
})();
