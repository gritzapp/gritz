const fields = ["serverUrl", "apiKey", "defaultWorkspace"];
const status = document.getElementById("status");

browser.storage.sync.get(fields).then((values) => {
  for (const f of fields) {
    document.getElementById(f).value = values[f] || "";
  }
});

document.getElementById("save").addEventListener("click", async () => {
  const values = {};
  for (const f of fields) {
    values[f] = document.getElementById(f).value.trim();
  }
  try {
    // The background script needs host permission for the gritz server to
    // call it without CORS. permissions.request must run in the click handler.
    const origin = new URL(values.serverUrl).origin;
    const granted = await browser.permissions.request({ origins: [`${origin}/*`] });
    if (!granted) {
      throw new Error(`permission for ${origin} was not granted`);
    }
    await browser.storage.sync.set(values);
    status.textContent = "Saved";
  } catch (err) {
    status.textContent = `Error: ${err.message}`;
  }
});
