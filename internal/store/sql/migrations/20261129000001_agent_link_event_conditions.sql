-- migrate:up

-- A link kind's events become entries of an event type and its conditions:
-- ["issue_comment"] becomes [{"type": "issue_comment"}]. A kind with no events
-- delivered every event type that declares its link type; it now delivers
-- only those it lists, so it gets the explicit list: the built-in event types
-- below, in registration order, and the org's custom event types.
WITH builtin (link_type, ord, event_type) AS (
    VALUES
        ('github.issue', 1, 'issue_comment'),
        ('github.issue', 2, 'issue_assigned'),
        ('github.issue', 3, 'label_added'),
        ('github.pr', 1, 'issue_comment'),
        ('github.pr', 2, 'pull_request_review_comment'),
        ('github.pr', 3, 'pull_request_review'),
        ('github.pr', 4, 'pull_request_assigned'),
        ('github.pr', 5, 'pull_request_opened'),
        ('github.pr', 6, 'pull_request_closed'),
        ('github.pr', 7, 'label_added'),
        ('github.pr', 8, 'pull_request_check_run_completed'),
        ('github.workflow_run', 1, 'check_run_completed'),
        ('jira.issue', 1, 'comment_created'),
        ('jira.issue', 2, 'issue_created'),
        ('jira.issue', 3, 'label_added'),
        ('jira.issue', 4, 'issue_assigned')
)
UPDATE agents a SET links = (
    SELECT jsonb_agg(
        CASE
            WHEN jsonb_array_length(COALESCE(link.l->'events', '[]')) > 0 THEN
                jsonb_set(link.l, '{events}', (
                    SELECT jsonb_agg(jsonb_build_object('type', ev.e) ORDER BY ev.i)
                    FROM jsonb_array_elements(link.l->'events') WITH ORDINALITY AS ev(e, i)
                ))
            WHEN declaring.events IS NULL THEN link.l - 'events'
            ELSE jsonb_set(link.l, '{events}', declaring.events)
        END
        ORDER BY link.n
    )
    FROM jsonb_array_elements(a.links) WITH ORDINALITY AS link(l, n)
    CROSS JOIN LATERAL (
        SELECT jsonb_agg(jsonb_build_object('type', t.event_type) ORDER BY t.ord) AS events
        FROM (
            SELECT b.ord, b.event_type
            FROM builtin b
            WHERE b.link_type = link.l->>'type'
            UNION ALL
            SELECT c.ord, c.def->>'type'
            FROM orgs o, jsonb_array_elements(o.custom_event_types) WITH ORDINALITY AS c(def, ord)
            WHERE o.id = a.org_id AND c.def->'link_types' ? (link.l->>'type')
        ) t
    ) declaring
)
WHERE jsonb_array_length(a.links) > 0;

-- migrate:down

UPDATE agents a SET links = (
    SELECT jsonb_agg(
        CASE
            WHEN link.l ? 'events' THEN
                jsonb_set(link.l, '{events}', (
                    SELECT jsonb_agg(ev.e->'type' ORDER BY ev.i)
                    FROM jsonb_array_elements(link.l->'events') WITH ORDINALITY AS ev(e, i)
                ))
            ELSE link.l
        END
        ORDER BY link.n
    )
    FROM jsonb_array_elements(a.links) WITH ORDINALITY AS link(l, n)
)
WHERE jsonb_array_length(a.links) > 0;
