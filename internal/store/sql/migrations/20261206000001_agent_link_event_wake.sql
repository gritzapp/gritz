-- migrate:up

-- Every event entry of every link kind wakes, as delivery always did.
UPDATE agents a SET links = (
    SELECT jsonb_agg(
        CASE
            WHEN jsonb_array_length(COALESCE(link.l->'events', '[]')) > 0 THEN
                jsonb_set(link.l, '{events}', (
                    SELECT jsonb_agg(ev.e || '{"wake": true}' ORDER BY ev.i)
                    FROM jsonb_array_elements(link.l->'events') WITH ORDINALITY AS ev(e, i)
                ))
            ELSE link.l
        END
        ORDER BY link.n
    )
    FROM jsonb_array_elements(a.links) WITH ORDINALITY AS link(l, n)
)
WHERE jsonb_array_length(a.links) > 0;

-- migrate:down

UPDATE agents a SET links = (
    SELECT jsonb_agg(
        CASE
            WHEN jsonb_array_length(COALESCE(link.l->'events', '[]')) > 0 THEN
                jsonb_set(link.l, '{events}', (
                    SELECT jsonb_agg(ev.e - 'wake' ORDER BY ev.i)
                    FROM jsonb_array_elements(link.l->'events') WITH ORDINALITY AS ev(e, i)
                ))
            ELSE link.l
        END
        ORDER BY link.n
    )
    FROM jsonb_array_elements(a.links) WITH ORDINALITY AS link(l, n)
)
WHERE jsonb_array_length(a.links) > 0;
