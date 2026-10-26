// Background script: owns the gritz credentials and makes the Connect RPC
// calls (JSON over HTTP POST), so the API key never reaches github.com.

async function settings() {
  const { serverUrl, apiKey } = await browser.storage.sync.get(["serverUrl", "apiKey"]);
  if (!serverUrl || !apiKey) {
    throw new Error("gritz is not configured: set the server URL and API key in the extension options");
  }
  return { serverUrl: serverUrl.replace(/\/+$/, ""), apiKey };
}

async function call(method, body) {
  const { serverUrl, apiKey } = await settings();
  const resp = await fetch(`${serverUrl}/gritz.v1.GritzService/${method}`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${apiKey}`,
    },
    body: JSON.stringify(body),
  });
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    throw new Error(data.message || `${method}: HTTP ${resp.status}`);
  }
  return data;
}

browser.runtime.onMessage.addListener(async (msg) => {
  switch (msg.type) {
    case "listWorkspaces": {
      const resp = await call("ListWorkspaces", {});
      return resp.workspaces || [];
    }
    case "createTask": {
      const resp = await call("CreateTask", {
        name: msg.name,
        workspace: msg.workspace,
        runner: msg.runner,
        instructions: [{ text: msg.text, url: msg.url }],
      });
      return resp.task;
    }
    case "openOptions":
      return browser.runtime.openOptionsPage();
  }
});
