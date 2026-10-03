{{- if .Started -}}
{{- if .Events -}}
# Task {{ .Task.GetId }} · {{ .Task.GetName }}

Since your last run, the task received new events:{{ range .Events }}

{{ renderEvent . }}{{ end }}

Continue working on the task.
{{- else -}}
The task was updated. Continue.
{{- end -}}
{{- else -}}
{{ renderHeader .Task }}

## How to work this task
{{ if not .Task.GetName }}This task has no name yet — set one with gritz:update_my_task.
{{ end }}If you have questions, problems, or take no action, respond on the platform from the most recent instruction or event url, suffixing your message with (task {{ .Task.GetId }}).
When you create a resource (PR, issue, comment), record it with gritz:create_link and subscribe=true so you receive replies, even after the task is complete. Use subscribe=false only for reference links you didn't create.
Prefer web URLs a user can visit over API URLs.
Use gritz:report to log important observations. Your text responses are not visible to users — only tool calls are.
If you need to re-check for updates mid-run, call gritz:get_my_task.
{{- if .Prompt}}

{{ .Prompt }}
{{- end }}

This is your first run on this task. Its full context is below — you already have everything you need and do not need to call get_my_task to begin.{{ range .Events }}

{{ renderEvent . }}{{ end }}{{ range .Links }}

{{ renderLink . }}{{ end }}
{{- end -}}
