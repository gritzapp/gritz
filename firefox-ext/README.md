# gritz for GitHub (Firefox)

Adds a **gritz** button next to the highlighted lines in the GitHub code view,
to the left of the Copilot button. Clicking it opens a dialog where you pick a
workspace, write a prompt, and create a task. The task's instruction contains
the prompt, a permalink to the selected lines, and the selected code.

## Install

1. Open `about:debugging#/runtime/this-firefox` and click **Load Temporary Add-on...**.
2. Select `firefox-ext/manifest.json`.
3. Open the add-on's preferences and set the server URL and an API key (create
   one in the gritz web UI). Saving asks for permission to reach the server.

## Use

Click a line number (shift-click for a range) on any `github.com/<owner>/<repo>/blob/...`
page, then click **gritz**.
