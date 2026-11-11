// Static fixtures only: HTMX never sends entered notebook settings or passwords.
(() => {
  const dialog = document.getElementById('notebook-dialog');
  const content = document.getElementById('notebook-dialog-content');
  let opener;

  function closeDialog() {
    dialog.close();
    content.replaceChildren();
    if (opener?.isConnected) opener.focus();
    else document.getElementById('add-notebook')?.focus();
  }

  function updateForm() {
    const encrypted = document.getElementById('notebook-encrypted');
    if (encrypted) {
      const fields = document.getElementById('notebook-crypt-fields');
      fields.hidden = !encrypted.checked;
      encrypted.setAttribute('aria-expanded', String(encrypted.checked));
      fields.querySelectorAll('input, select').forEach(field => {
        field.disabled = !encrypted.checked;
      });
      document.getElementById('crypt-password').required = encrypted.checked;
      const initialize = document.getElementById('crypt-new-prefix').checked;
      const verify = document.getElementById('crypt-verify');
      document.getElementById('crypt-existing-file').hidden = initialize;
      verify.disabled = !encrypted.checked || initialize;
      verify.required = encrypted.checked && !initialize;
    }
    const connection = document.getElementById('notebook-connection');
    if (connection) {
      document.getElementById('notebook-access-help').textContent = connection.value === 'archive'
        ? 'This connection is read-only. The notebook stays read-only even when this switch is off.'
        : 'A read-only connection always takes precedence.';
    }
  }

  document.addEventListener('change', updateForm);
  document.addEventListener('htmx:beforeRequest', event => {
    if (event.detail.target === content) opener = event.detail.elt;
  });
  document.addEventListener('htmx:afterSwap', event => {
    if (event.detail.target === content && content.childElementCount && !dialog.open) {
      updateForm();
      dialog.showModal();
      document.getElementById('notebook-name')?.focus();
    }
    if (event.detail.target.id === 'notebook-area') {
      if (dialog.open) closeDialog();
      document.getElementById('add-notebook')?.focus();
    }
  });
  document.addEventListener('click', event => {
    if (event.target.closest('[data-close-notebook]')) closeDialog();
  });
  dialog.addEventListener('cancel', event => {
    event.preventDefault();
    closeDialog();
  });
  updateForm();
})();
