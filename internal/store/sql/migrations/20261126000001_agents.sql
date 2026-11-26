-- migrate:up

-- Agents. See proposals/draft/agents.md.
CREATE TABLE agents (
    id           bigserial PRIMARY KEY,
    org_id       bigint NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    name         text NOT NULL,
    workspace    text NOT NULL,
    runner       text NOT NULL,
    prompt       text NOT NULL DEFAULT '',
    auto_archive bigint NOT NULL DEFAULT 0,
    variables    jsonb NOT NULL DEFAULT '{}',
    triggers     jsonb NOT NULL DEFAULT '[]',
    links        jsonb NOT NULL DEFAULT '[]',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_agents_org_id ON agents (org_id);

-- migrate:down

DROP TABLE IF EXISTS agents;
