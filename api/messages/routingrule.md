# RoutingRule

## Fields

| Field | Type | Description |
| --- | --- | --- |
| `source` | `string` | |
| `type` | `string` | |
| `create` | [CreateTaskAction](/api/messages/createtaskaction/) | |
| `wakeup` | `bool` | Whether a match starts the subscribed tasks again to handle the event. When false, the event is added to their timelines without starting them. Rules created in the web UI default to true. |
| `conditions` | repeated [RuleCondition](/api/messages/rulecondition/) | Conditions the event must meet, all of them. Empty source and type match any; no conditions match every event of the selected type. |
| `public` | `bool` | Whether the rule also acts on events from people who aren't members of the org. False (the default) acts only on members' events. |
| `namespace` | `string` | The rule's namespace: a match looks for subscribed tasks only in it, and a task the rule creates is in it. Empty is the default namespace. |