-- migrate:up

-- Cutover to agents. See proposals/draft/agents.md.
--
-- Every org gets an agent named "default" with no triggers or links, and every
-- existing task and schedule is assigned to its org's one. Existing links get
-- no kind, so they become plain references and stop receiving events, and the
-- routing rules are dropped: neither is migrated into agents.

-- A deleted agent keeps its row, so the archived tasks of it keep their
-- reference; it is live while deleted_at is NULL.
ALTER TABLE agents ADD COLUMN deleted_at timestamptz;

ALTER TABLE tasks ADD COLUMN agent_id bigint REFERENCES agents(id);
ALTER TABLE schedules ADD COLUMN agent_id bigint REFERENCES agents(id);

WITH defaults AS (
    INSERT INTO agents (org_id, name, workspace, runner)
    SELECT id, 'default', '', '' FROM orgs
    RETURNING id, org_id
), tasks_assigned AS (
    UPDATE tasks t SET agent_id = d.id FROM defaults d WHERE t.org_id = d.org_id
)
UPDATE schedules s SET agent_id = d.id FROM defaults d WHERE s.org_id = d.org_id;

ALTER TABLE tasks ALTER COLUMN agent_id SET NOT NULL;
CREATE INDEX idx_tasks_agent_id ON tasks (agent_id) WHERE archived = FALSE;
ALTER TABLE tasks DROP COLUMN namespace;

ALTER TABLE schedules ALTER COLUMN agent_id SET NOT NULL;
CREATE INDEX idx_schedules_agent_id ON schedules (agent_id);
ALTER TABLE schedules DROP COLUMN namespace;

-- A link with a kind is subscribed; an empty kind is a plain reference. The
-- claim is copied from the kind when the link is created.
ALTER TABLE task_links ADD COLUMN kind text NOT NULL DEFAULT '';
ALTER TABLE task_links ADD COLUMN claim text NOT NULL DEFAULT '';
ALTER TABLE task_links DROP COLUMN routing_key;
ALTER TABLE task_links DROP COLUMN subscribe;
CREATE INDEX idx_task_links_subscribed ON task_links (url) WHERE kind <> '';

ALTER TABLE orgs DROP COLUMN routing_rules;

-- migrate:down

ALTER TABLE orgs ADD COLUMN routing_rules jsonb NOT NULL DEFAULT '[]';

DROP INDEX IF EXISTS idx_task_links_subscribed;
ALTER TABLE task_links ADD COLUMN subscribe boolean NOT NULL DEFAULT FALSE;
ALTER TABLE task_links ADD COLUMN routing_key text NOT NULL DEFAULT '';
CREATE INDEX idx_task_links_routing_key ON task_links (routing_key);
ALTER TABLE task_links DROP COLUMN claim;
ALTER TABLE task_links DROP COLUMN kind;

ALTER TABLE schedules ADD COLUMN namespace text NOT NULL DEFAULT '';
ALTER TABLE schedules DROP COLUMN agent_id;

ALTER TABLE tasks ADD COLUMN namespace text NOT NULL DEFAULT '';
ALTER TABLE tasks DROP COLUMN agent_id;

DELETE FROM agents;
ALTER TABLE agents DROP COLUMN deleted_at;
