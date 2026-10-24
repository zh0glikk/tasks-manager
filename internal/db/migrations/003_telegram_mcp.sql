-- messages from the group chat, kept for a limited time (see CHAT_RETENTION_DAYS)
create table tg_messages (
    id         bigserial primary key,
    chat_id    bigint      not null,
    message_id bigint      not null,
    from_id    bigint,
    from_name  text        not null default '',
    text       text        not null default '',
    reply_to   bigint,
    sent_at    timestamptz not null,
    edited_at  timestamptz,
    unique (chat_id, message_id)
);
create index tg_messages_sent_idx on tg_messages (chat_id, sent_at);

-- personal MCP keys; only a SHA-256 of the key is stored
create table api_keys (
    id           bigserial primary key,
    tg_user_id   bigint,
    name         text        not null,
    key_hash     bytea       not null unique,
    scope        text        not null default 'write',
    created_at   timestamptz not null default now(),
    revoked_at   timestamptz,
    last_used_at timestamptz
);
create index api_keys_user_idx on api_keys (tg_user_id) where revoked_at is null;

-- who called which MCP tool, purged with the chat messages
create table mcp_access (
    id         bigserial primary key,
    key_id     bigint      not null references api_keys (id),
    tool       text        not null,
    created_at timestamptz not null default now()
);
create index mcp_access_created_idx on mcp_access (created_at);

-- small persistent settings (bot update offset, last reminder date)
create table kv (
    key        text primary key,
    value      text        not null,
    updated_at timestamptz not null default now()
);
