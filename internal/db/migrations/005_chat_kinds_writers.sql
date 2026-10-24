-- main chats grant keys and get reminders/commands; watch chats are only stored
alter table tg_chats add column kind text not null default 'main'; -- main | watch

-- people allowed to write through MCP (admins always can)
create table tg_writers (
    tg_user_id bigint primary key,
    name       text        not null default '',
    granted_at timestamptz not null default now()
);
