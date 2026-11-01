\restrict dbmate

-- Dumped from database version 18.6
-- Dumped by pg_dump version 18.6 (Debian 18.6-1.pgdg13+2)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.events (
    id bigint NOT NULL,
    org_id bigint NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    task_id bigint NOT NULL,
    type text NOT NULL,
    wake boolean DEFAULT false NOT NULL,
    payload jsonb NOT NULL,
    xid xid8 DEFAULT pg_current_xact_id() NOT NULL
);


--
-- Name: events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.events_id_seq OWNED BY public.events.id;


--
-- Name: keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.keys (
    id uuid NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    token_hash text NOT NULL,
    org_id bigint NOT NULL,
    expires_at timestamp without time zone,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    scopes text[] DEFAULT ARRAY['*.*'::text]
);


--
-- Name: log_chunks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.log_chunks (
    id bigint NOT NULL,
    org_id bigint NOT NULL,
    task_id bigint NOT NULL,
    version bigint NOT NULL,
    data bytea NOT NULL,
    created_at timestamp without time zone DEFAULT (now() AT TIME ZONE 'UTC'::text) NOT NULL
);


--
-- Name: log_chunks_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.log_chunks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: log_chunks_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.log_chunks_id_seq OWNED BY public.log_chunks.id;


--
-- Name: org_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.org_members (
    org_id bigint NOT NULL,
    user_id text NOT NULL,
    role text DEFAULT 'member'::text NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: orgs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.orgs (
    id bigint NOT NULL,
    name text NOT NULL,
    owner text NOT NULL,
    archived boolean DEFAULT false NOT NULL,
    atlassian_webhook_secret text DEFAULT ''::text NOT NULL,
    routing_rules jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    github_installation_id bigint
);


--
-- Name: orgs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.orgs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: orgs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.orgs_id_seq OWNED BY public.orgs.id;


--
-- Name: schedules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schedules (
    id bigint NOT NULL,
    org_id bigint NOT NULL,
    created_by text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    workspace text NOT NULL,
    runner text NOT NULL,
    namespace text DEFAULT ''::text NOT NULL,
    instructions jsonb DEFAULT '[]'::jsonb NOT NULL,
    auto_archive bigint DEFAULT 0 NOT NULL,
    cron_expr text NOT NULL,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    next_run_at timestamp without time zone,
    last_run_at timestamp without time zone,
    last_task_id bigint,
    created_at timestamp without time zone DEFAULT (now() AT TIME ZONE 'UTC'::text) NOT NULL,
    updated_at timestamp without time zone DEFAULT (now() AT TIME ZONE 'UTC'::text) NOT NULL
);


--
-- Name: schedules_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.schedules_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: schedules_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.schedules_id_seq OWNED BY public.schedules.id;


--
-- Name: schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migrations (
    version character varying NOT NULL
);


--
-- Name: task_links; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.task_links (
    id bigint NOT NULL,
    task_id bigint NOT NULL,
    relevance text NOT NULL,
    url text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    subscribe boolean DEFAULT false NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    routing_key text DEFAULT ''::text NOT NULL
);


--
-- Name: task_links_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.task_links_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: task_links_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.task_links_id_seq OWNED BY public.task_links.id;


--
-- Name: tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tasks (
    id bigint NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    runner text NOT NULL,
    workspace text NOT NULL,
    status integer NOT NULL,
    command integer DEFAULT 0 NOT NULL,
    version bigint DEFAULT 0 NOT NULL,
    org_id bigint NOT NULL,
    archived boolean DEFAULT false NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    auto_archive bigint DEFAULT 0 CONSTRAINT tasks_archive_after_not_null NOT NULL,
    shell_session text DEFAULT ''::text NOT NULL,
    namespace text DEFAULT ''::text NOT NULL
);


--
-- Name: tasks_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tasks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tasks_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tasks_id_seq OWNED BY public.tasks.id;


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id text NOT NULL,
    email text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    github_user_id bigint,
    github_username text,
    atlassian_account_id text,
    atlassian_username text DEFAULT ''::text NOT NULL,
    default_org_id bigint,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: workspaces; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.workspaces (
    id bigint NOT NULL,
    runner_id text NOT NULL,
    name text NOT NULL,
    org_id bigint NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    updated_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);


--
-- Name: workspaces_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.workspaces_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: workspaces_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.workspaces_id_seq OWNED BY public.workspaces.id;


--
-- Name: events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.events ALTER COLUMN id SET DEFAULT nextval('public.events_id_seq'::regclass);


--
-- Name: log_chunks id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.log_chunks ALTER COLUMN id SET DEFAULT nextval('public.log_chunks_id_seq'::regclass);


--
-- Name: orgs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.orgs ALTER COLUMN id SET DEFAULT nextval('public.orgs_id_seq'::regclass);


--
-- Name: schedules id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedules ALTER COLUMN id SET DEFAULT nextval('public.schedules_id_seq'::regclass);


--
-- Name: task_links id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.task_links ALTER COLUMN id SET DEFAULT nextval('public.task_links_id_seq'::regclass);


--
-- Name: tasks id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tasks ALTER COLUMN id SET DEFAULT nextval('public.tasks_id_seq'::regclass);


--
-- Name: workspaces id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspaces ALTER COLUMN id SET DEFAULT nextval('public.workspaces_id_seq'::regclass);


--
-- Name: events events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.events
    ADD CONSTRAINT events_pkey PRIMARY KEY (id);


--
-- Name: keys keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.keys
    ADD CONSTRAINT keys_pkey PRIMARY KEY (id);


--
-- Name: log_chunks log_chunks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.log_chunks
    ADD CONSTRAINT log_chunks_pkey PRIMARY KEY (id);


--
-- Name: org_members org_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.org_members
    ADD CONSTRAINT org_members_pkey PRIMARY KEY (org_id, user_id);


--
-- Name: orgs orgs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.orgs
    ADD CONSTRAINT orgs_pkey PRIMARY KEY (id);


--
-- Name: schedules schedules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedules
    ADD CONSTRAINT schedules_pkey PRIMARY KEY (id);


--
-- Name: schema_migrations schema_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_migrations
    ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);


--
-- Name: task_links task_links_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.task_links
    ADD CONSTRAINT task_links_pkey PRIMARY KEY (id);


--
-- Name: tasks tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tasks
    ADD CONSTRAINT tasks_pkey PRIMARY KEY (id);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: workspaces workspaces_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspaces
    ADD CONSTRAINT workspaces_pkey PRIMARY KEY (id);


--
-- Name: idx_events_org_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_events_org_id ON public.events USING btree (org_id);


--
-- Name: idx_events_task_id_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_events_task_id_id ON public.events USING btree (task_id, id);


--
-- Name: idx_events_xid_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_events_xid_id ON public.events USING btree (xid, id);


--
-- Name: idx_keys_org_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_keys_org_id ON public.keys USING btree (org_id);


--
-- Name: idx_keys_token_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_keys_token_hash ON public.keys USING btree (token_hash);


--
-- Name: idx_log_chunks_task_id_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_log_chunks_task_id_id ON public.log_chunks USING btree (task_id, id);


--
-- Name: idx_org_members_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_org_members_user_id ON public.org_members USING btree (user_id);


--
-- Name: idx_orgs_github_installation_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_orgs_github_installation_id ON public.orgs USING btree (github_installation_id);


--
-- Name: idx_orgs_owner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_orgs_owner ON public.orgs USING btree (owner);


--
-- Name: idx_schedules_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_schedules_due ON public.schedules USING btree (next_run_at) WHERE ((enabled = true) AND (next_run_at IS NOT NULL));


--
-- Name: idx_schedules_org_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_schedules_org_id ON public.schedules USING btree (org_id);


--
-- Name: idx_task_links_routing_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_task_links_routing_key ON public.task_links USING btree (routing_key);


--
-- Name: idx_task_links_task_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_task_links_task_id ON public.task_links USING btree (task_id);


--
-- Name: idx_tasks_archive_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tasks_archive_due ON public.tasks USING btree (updated_at) WHERE ((archived = false) AND (auto_archive <> 0));


--
-- Name: idx_tasks_archived; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tasks_archived ON public.tasks USING btree (archived);


--
-- Name: idx_tasks_org_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tasks_org_id ON public.tasks USING btree (org_id);


--
-- Name: idx_tasks_runner_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tasks_runner_status ON public.tasks USING btree (runner, status);


--
-- Name: idx_tasks_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tasks_status ON public.tasks USING btree (status);


--
-- Name: idx_users_atlassian_account_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_users_atlassian_account_id ON public.users USING btree (atlassian_account_id);


--
-- Name: idx_users_email; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_users_email ON public.users USING btree (email);


--
-- Name: idx_users_github_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_users_github_user_id ON public.users USING btree (github_user_id);


--
-- Name: idx_workspaces_org_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_workspaces_org_id ON public.workspaces USING btree (org_id);


--
-- Name: idx_workspaces_runner_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_workspaces_runner_id ON public.workspaces USING btree (runner_id);


--
-- Name: tasks_org_created_id_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tasks_org_created_id_active_idx ON public.tasks USING btree (org_id, created_at DESC, id DESC) WHERE (archived = false);


--
-- Name: tasks_org_created_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tasks_org_created_id_idx ON public.tasks USING btree (org_id, created_at DESC, id DESC);


--
-- Name: events fk_events_org_id; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.events
    ADD CONSTRAINT fk_events_org_id FOREIGN KEY (org_id) REFERENCES public.orgs(id) ON DELETE CASCADE;


--
-- Name: events fk_events_task_id; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.events
    ADD CONSTRAINT fk_events_task_id FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;


--
-- Name: keys fk_keys_org_id; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.keys
    ADD CONSTRAINT fk_keys_org_id FOREIGN KEY (org_id) REFERENCES public.orgs(id) ON DELETE CASCADE;


--
-- Name: orgs fk_orgs_owner; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.orgs
    ADD CONSTRAINT fk_orgs_owner FOREIGN KEY (owner) REFERENCES public.users(id);


--
-- Name: tasks fk_tasks_org_id; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tasks
    ADD CONSTRAINT fk_tasks_org_id FOREIGN KEY (org_id) REFERENCES public.orgs(id) ON DELETE CASCADE;


--
-- Name: users fk_users_default_org_id; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT fk_users_default_org_id FOREIGN KEY (default_org_id) REFERENCES public.orgs(id) ON DELETE SET NULL;


--
-- Name: workspaces fk_workspaces_org_id; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspaces
    ADD CONSTRAINT fk_workspaces_org_id FOREIGN KEY (org_id) REFERENCES public.orgs(id) ON DELETE CASCADE;


--
-- Name: log_chunks log_chunks_org_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.log_chunks
    ADD CONSTRAINT log_chunks_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.orgs(id) ON DELETE CASCADE;


--
-- Name: log_chunks log_chunks_task_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.log_chunks
    ADD CONSTRAINT log_chunks_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;


--
-- Name: org_members org_members_org_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.org_members
    ADD CONSTRAINT org_members_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.orgs(id) ON DELETE CASCADE;


--
-- Name: org_members org_members_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.org_members
    ADD CONSTRAINT org_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id);


--
-- Name: schedules schedules_created_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedules
    ADD CONSTRAINT schedules_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(id);


--
-- Name: schedules schedules_last_task_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedules
    ADD CONSTRAINT schedules_last_task_id_fkey FOREIGN KEY (last_task_id) REFERENCES public.tasks(id) ON DELETE SET NULL;


--
-- Name: schedules schedules_org_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schedules
    ADD CONSTRAINT schedules_org_id_fkey FOREIGN KEY (org_id) REFERENCES public.orgs(id) ON DELETE CASCADE;


--
-- Name: task_links task_links_task_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.task_links
    ADD CONSTRAINT task_links_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.tasks(id) ON DELETE CASCADE;


--
-- PostgreSQL database dump complete
--

\unrestrict dbmate


--
-- Dbmate schema migrations
--

INSERT INTO public.schema_migrations (version) VALUES
    ('20240101000001'),
    ('20260517000001'),
    ('20260517174647'),
    ('20260523000001'),
    ('20260601000001'),
    ('20260607000001'),
    ('20260607000002'),
    ('20260613000001'),
    ('20260613120000'),
    ('20260614000001'),
    ('20260614000002'),
    ('20260614000003'),
    ('20260621000001'),
    ('20260621000002'),
    ('20260701000001'),
    ('20260705000001'),
    ('20260711000001'),
    ('20260712000001'),
    ('20260712000002'),
    ('20260712000003'),
    ('20260718000001'),
    ('20260718000002'),
    ('20260906000001'),
    ('20261101000001');
