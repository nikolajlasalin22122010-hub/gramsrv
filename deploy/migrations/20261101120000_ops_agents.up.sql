-- Ops agents: a user installs daskgram-ops on their own VDS, enrolls with a
-- one-time code from the bot, and then polls us outbound. We never connect to
-- their host and never store a password: the agent holds a token, we hold its
-- hash.
create table if not exists ops_agents (
  id            bigserial primary key,
  user_id       bigint not null,
  name          text not null default '',
  token_hash    bytea not null,
  created_at    timestamptz not null default now(),
  last_seen_at  timestamptz,
  revoked_at    timestamptz
);

create unique index if not exists ops_agents_token_hash_key on ops_agents (token_hash);
create index if not exists ops_agents_user_idx on ops_agents (user_id) where revoked_at is null;

-- One-time enrollment codes. Only the hash is stored, so a leaked database dump
-- does not hand out working codes.
create table if not exists ops_enroll_codes (
  code_hash   bytea primary key,
  user_id     bigint not null,
  created_at  timestamptz not null default now(),
  expires_at  timestamptz not null,
  used_at     timestamptz
);

create index if not exists ops_enroll_codes_user_idx on ops_enroll_codes (user_id);

-- Jobs are created by the bot and claimed by the agent that polls next.
create table if not exists ops_jobs (
  id           bigserial primary key,
  agent_id     bigint not null references ops_agents (id) on delete cascade,
  kind         text not null,
  status       text not null default 'pending',
  created_at   timestamptz not null default now(),
  started_at   timestamptz,
  finished_at  timestamptz,
  output       text not null default ''
);

create index if not exists ops_jobs_agent_pending_idx on ops_jobs (agent_id, status);
