-- migrate:up

-- public moves from a link kind onto each of its event entries: a kind that
-- was public gets every event entry marked public, and loses its own flag.
UPDATE agents a SET links = (
    SELECT jsonb_agg(
        CASE
            WHEN (link.l->>'public')::boolean AND jsonb_array_length(COALESCE(link.l->'events', '[]')) > 0 THEN
                jsonb_set(link.l - 'public', '{events}', (
                    SELECT jsonb_agg(ev.e || '{"public": true}' ORDER BY ev.i)
                    FROM jsonb_array_elements(link.l->'events') WITH ORDINALITY AS ev(e, i)
                ))
            ELSE link.l - 'public'
        END
        ORDER BY link.n
    )
    FROM jsonb_array_elements(a.links) WITH ORDINALITY AS link(l, n)
)
WHERE jsonb_array_length(a.links) > 0;

-- migrate:down

-- A kind is public when any of its event entries is.
UPDATE agents a SET links = (
    SELECT jsonb_agg(
        CASE
            WHEN link.l ? 'events' THEN
                jsonb_set(link.l, '{events}', (
                    SELECT jsonb_agg(ev.e - 'public' ORDER BY ev.i)
                    FROM jsonb_array_elements(link.l->'events') WITH ORDINALITY AS ev(e, i)
                )) || CASE
                    WHEN EXISTS (
                        SELECT 1 FROM jsonb_array_elements(link.l->'events') ev(e)
                        WHERE (ev.e->>'public')::boolean
                    ) THEN '{"public": true}'::jsonb
                    ELSE '{}'::jsonb
                END
            ELSE link.l
        END
        ORDER BY link.n
    )
    FROM jsonb_array_elements(a.links) WITH ORDINALITY AS link(l, n)
)
WHERE jsonb_array_length(a.links) > 0;
