# ListRunnerEventsResponse

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `events` | repeated [Event](/api/messages/event/) | |
| `next_page_token` | `string` | Resumes after the last event in this page; the request's page_token when the page is empty. Pass it back unchanged. |
| `more` | `bool` | Whether more events are available right now past this page. |