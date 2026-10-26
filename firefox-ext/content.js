// Content script: adds a gritz button next to the highlighted lines in the
// GitHub code view (to the left of the Copilot button), and a dialog that
// creates a task with the selected code as context.

const LINE_HEIGHT = 20;

let button = null;
let dialog = null;

// Parses the selected line range from the URL hash: #L17 or #L17-L25.
function selectedLines() {
  const m = location.hash.match(/^#L(\d+)(?:C\d+)?(?:-L(\d+)(?:C\d+)?)?$/);
  if (!m) {
    return null;
  }
  const start = Number(m[1]);
  const end = m[2] ? Number(m[2]) : start;
  return { start: Math.min(start, end), end: Math.max(start, end) };
}

// Returns the repo, path, and commit of the current blob, read from the
// payload GitHub embeds for its React code view. That payload is not updated
// on client-side navigation, so it is only trusted when its path matches the
// URL; otherwise the path comes from the URL and there is no commit.
function blobInfo() {
  const [, owner, repo, , ...rest] = location.pathname.split("/");
  const urlPath = decodeURIComponent(rest.join("/"));
  const info = { repo: `${owner}/${repo}`, path: rest.slice(1).join("/"), commit: null };
  for (const el of document.querySelectorAll('script[data-target="react-app.embeddedData"]')) {
    try {
      for (const route of Object.values(JSON.parse(el.textContent).payload)) {
        if (route?.path && route.refInfo?.currentOid && urlPath.endsWith(`/${route.path}`)) {
          info.path = route.path;
          info.commit = route.refInfo.currentOid;
        }
      }
    } catch {
      // not the payload we want
    }
  }
  return info;
}

function permalink(info, lines) {
  const range = lines.start === lines.end ? `L${lines.start}` : `L${lines.start}-L${lines.end}`;
  if (info.commit) {
    return `https://github.com/${info.repo}/blob/${info.commit}/${info.path}#${range}`;
  }
  return location.href.split("#")[0] + `#${range}`;
}

function selectedCode(lines) {
  const textarea = document.getElementById("read-only-cursor-text-area");
  if (!textarea) {
    return "";
  }
  return textarea.value.split("\n").slice(lines.start - 1, lines.end).join("\n");
}

function update() {
  const positioner = document.getElementById("copilot-button-positioner");
  const lines = selectedLines();
  if (!positioner || !lines) {
    button?.remove();
    return;
  }
  if (!button) {
    button = document.createElement("button");
    button.type = "button";
    button.className = "gritz-line-button";
    button.textContent = "gritz";
    button.title = "Start a gritz task from the selected lines";
    button.addEventListener("click", (e) => {
      e.preventDefault();
      e.stopPropagation();
      openDialog();
    });
  }
  if (button.parentElement !== positioner) {
    positioner.appendChild(button);
  }
  button.style.top = `${(lines.start - 1) * LINE_HEIGHT - 2}px`;
}

function el(tag, props = {}, children = []) {
  const node = Object.assign(document.createElement(tag), props);
  node.append(...children);
  return node;
}

async function openDialog() {
  const lines = selectedLines();
  if (!lines) {
    return;
  }
  const info = blobInfo();
  const url = permalink(info, lines);
  const code = selectedCode(lines);
  const range = lines.start === lines.end ? `L${lines.start}` : `L${lines.start}-L${lines.end}`;

  dialog?.remove();
  const workspace = el("select", { required: true }, [el("option", { textContent: "Loading..." })]);
  const name = el("input", { type: "text", value: `${info.path}#${range}` });
  const prompt = el("textarea", { rows: 6, required: true, placeholder: "What should the agent do?" });
  const status = el("div", { className: "gritz-status" });
  const submit = el("button", { type: "submit", className: "btn btn-primary", textContent: "Create task" });
  const cancel = el("button", { type: "button", className: "btn", textContent: "Cancel" });
  const form = el("form", { method: "dialog" }, [
    el("h3", { textContent: "New gritz task" }),
    el("div", { className: "gritz-context", textContent: `${info.repo} ${info.path}#${range}` }),
    el("label", { textContent: "Workspace" }, [workspace]),
    el("label", { textContent: "Name" }, [name]),
    el("label", { textContent: "Prompt" }, [prompt]),
    status,
    el("div", { className: "gritz-actions" }, [cancel, submit]),
  ]);
  dialog = el("dialog", { className: "gritz-dialog" }, [form]);
  document.body.appendChild(dialog);
  dialog.showModal();
  prompt.focus();

  cancel.addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => dialog.remove());
  // Keep GitHub's code view keyboard shortcuts from firing while typing.
  dialog.addEventListener("keydown", (e) => e.stopPropagation());

  try {
    const [workspaces, { defaultWorkspace }] = await Promise.all([
      browser.runtime.sendMessage({ type: "listWorkspaces" }),
      browser.storage.sync.get("defaultWorkspace"),
    ]);
    workspace.replaceChildren(
      ...workspaces.map((w) =>
        el("option", {
          value: JSON.stringify({ name: w.name, runner: w.runnerId }),
          textContent: w.description ? `${w.name} - ${w.description}` : w.name,
          selected: w.name === defaultWorkspace,
        }),
      ),
    );
  } catch (err) {
    workspace.replaceChildren(el("option", { textContent: "(unavailable)" }));
    showError(status, err);
    return;
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const ws = JSON.parse(workspace.value);
    const text = `${prompt.value.trim()}\n\nContext: ${info.repo} \`${info.path}\` lines ${lines.start}-${lines.end} (${url})\n\n\`\`\`\n${code}\n\`\`\``;
    submit.disabled = true;
    status.textContent = "Creating...";
    try {
      const task = await browser.runtime.sendMessage({
        type: "createTask",
        name: name.value.trim(),
        workspace: ws.name,
        runner: ws.runner,
        text,
        url,
      });
      status.replaceChildren(
        "Created ",
        el("a", { href: task.url, target: "_blank", rel: "noopener", textContent: `task #${task.id}` }),
      );
      submit.remove();
      cancel.textContent = "Close";
    } catch (err) {
      submit.disabled = false;
      showError(status, err);
    }
  });
}

function showError(status, err) {
  status.replaceChildren(el("span", { className: "gritz-error", textContent: err.message }));
  if (/not configured/.test(err.message)) {
    const link = el("a", { href: "#", textContent: " Open options" });
    link.addEventListener("click", (e) => {
      e.preventDefault();
      browser.runtime.sendMessage({ type: "openOptions" });
    });
    status.append(link);
  }
}

// GitHub is a single-page app: the code view mounts and re-renders without a
// page load, and line selection only changes the hash.
let scheduled = false;
new MutationObserver(() => {
  if (!scheduled) {
    scheduled = true;
    requestAnimationFrame(() => {
      scheduled = false;
      update();
    });
  }
}).observe(document.body, { childList: true, subtree: true });
window.addEventListener("hashchange", update);
window.addEventListener("popstate", update);
update();
