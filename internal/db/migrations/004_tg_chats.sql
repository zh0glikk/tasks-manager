-- chats the bot is in; messages are stored only for allowed ones
create table tg_chats (
    chat_id    bigint primary key,
    title      text        not null default '',
    status     text        not null default 'pending', -- pending | allowed | denied
    reminders  boolean     not null default true,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);
