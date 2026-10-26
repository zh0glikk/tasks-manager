-- recorded classes and meetings; readable only with a key
create table lectures (
    id         bigserial primary key,
    event_id   bigint references events (id),  -- the class in the calendar, if any
    course_id  bigint references courses (id),
    date       date        not null,
    title      text        not null default '',
    video_url  text        not null default '', -- YouTube, Drive, Zoom, Telegram…
    duration_s int         not null default 0,
    summary    text        not null default '',
    author     text        not null default '',
    created_at timestamptz not null default now(),
    deleted_at timestamptz
);
create index lectures_date_idx on lectures (date) where deleted_at is null;
create index lectures_event_idx on lectures (event_id);

-- transcript pieces with timecodes; kept out of the change log
create table lecture_segments (
    lecture_id bigint not null references lectures (id),
    idx        int    not null,
    start_s    int    not null,
    end_s      int    not null,
    text       text   not null,
    primary key (lecture_id, idx)
);
-- 'simple': there is no built-in Ukrainian stemmer, search uses word prefixes instead
create index lecture_segments_fts_idx on lecture_segments using gin (to_tsvector('simple', text));
