create table courses (
    id         bigserial primary key,
    name       text        not null,
    short      text        not null,
    teacher    text        not null default '',
    link       text        not null default '',
    color      text        not null default '#0E5D69',
    created_at timestamptz not null default now(),
    deleted_at timestamptz
);

-- events are scheduled classes; a weekly series shares series_id
create table events (
    id         bigserial primary key,
    series_id  bigint,
    course_id  bigint references courses (id),
    kind       text        not null default '',
    date       date        not null,
    start_time time        not null,
    end_time   time,
    teacher    text        not null default '',
    link       text        not null default '',
    note       text        not null default '',
    tentative  boolean     not null default false,
    created_at timestamptz not null default now(),
    deleted_at timestamptz
);
create index events_date_idx on events (date) where deleted_at is null;
create index events_series_idx on events (series_id);

create table assignments (
    id          bigserial primary key,
    course_id   bigint references courses (id),
    title       text        not null,
    description text        not null default '',
    due_date    date        not null,
    due_time    time,
    link        text        not null default '',
    author      text        not null default '',
    created_at  timestamptz not null default now(),
    deleted_at  timestamptz
);
create index assignments_due_idx on assignments (due_date) where deleted_at is null;

create table posts (
    id         bigserial primary key,
    course_id  bigint references courses (id),
    title      text        not null,
    body       text        not null default '',
    pinned     boolean     not null default false,
    author     text        not null default '',
    created_at timestamptz not null default now(),
    deleted_at timestamptz
);

-- audit log: every write lands here so anonymous edits can be reverted
create table changes (
    id          bigserial primary key,
    entity      text        not null,
    entity_id   bigint      not null,
    action      text        not null,
    author      text        not null default '',
    summary     text        not null default '',
    before      jsonb,
    after       jsonb,
    created_at  timestamptz not null default now(),
    reverted_at timestamptz
);
create index changes_created_idx on changes (created_at desc);
