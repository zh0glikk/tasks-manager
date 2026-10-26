package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- chat messages ----

type ChatMessage struct {
	ChatID    int64
	MessageID int64
	FromID    int64
	FromName  string
	Text      string
	ReplyTo   *int64
	SentAt    time.Time
	EditedAt  *time.Time
}

// SaveMessage inserts a message or, for an edit, replaces its text.
func (s *Store) SaveMessage(ctx context.Context, m ChatMessage) error {
	_, err := s.db.Exec(ctx, `insert into tg_messages (chat_id, message_id, from_id, from_name, text, reply_to, sent_at, edited_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
		on conflict (chat_id, message_id) do update set text = excluded.text, edited_at = excluded.edited_at`,
		m.ChatID, m.MessageID, m.FromID, m.FromName, m.Text, m.ReplyTo, m.SentAt, m.EditedAt)
	return err
}

func (s *Store) ListMessages(ctx context.Context, chatIDs []int64, since, until time.Time, limit int) ([]ChatMessage, error) {
	// newest `limit` messages in the window, returned oldest first
	rows, err := s.db.Query(ctx, `select chat_id, message_id, coalesce(from_id, 0), from_name, text, reply_to, sent_at, edited_at from (
			select * from tg_messages where chat_id = any($1) and sent_at >= $2 and sent_at < $3
			order by sent_at desc, message_id desc limit $4
		) m order by sent_at, message_id`, chatIDs, since, until, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row pgx.Row) (ChatMessage, error) {
		var m ChatMessage
		err := row.Scan(&m.ChatID, &m.MessageID, &m.FromID, &m.FromName, &m.Text, &m.ReplyTo, &m.SentAt, &m.EditedAt)
		return m, err
	})
}

// ImportMessages stores messages from a chat export that are older than the
// first message the bot saw live in that chat, so the two never overlap.
// Re-running it is harmless. It returns how many messages were imported.
func (s *Store) ImportMessages(ctx context.Context, chatID int64, msgs []ChatMessage) (int, error) {
	var firstLive *time.Time
	if err := s.db.QueryRow(ctx, `select min(sent_at) from tg_messages where chat_id = $1 and message_id > 0`, chatID).Scan(&firstLive); err != nil {
		return 0, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	n := 0
	for _, m := range msgs {
		if firstLive != nil && !m.SentAt.Before(*firstLive) {
			continue
		}
		if _, err := tx.Exec(ctx, `insert into tg_messages (chat_id, message_id, from_id, from_name, text, reply_to, sent_at, edited_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (chat_id, message_id) do update set text = excluded.text, edited_at = excluded.edited_at`,
			m.ChatID, m.MessageID, m.FromID, m.FromName, m.Text, m.ReplyTo, m.SentAt, m.EditedAt); err != nil {
			return 0, err
		}
		n++
	}
	return n, tx.Commit(ctx)
}

// Purge drops chat messages and MCP access records older than before.
func (s *Store) Purge(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `delete from tg_messages where sent_at < $1`, before)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(ctx, `delete from mcp_access where created_at < $1`, before); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---- chats ----

const (
	ChatPending = "pending"
	ChatAllowed = "allowed"
	ChatDenied  = "denied"

	ChatMain  = "main"  // members get keys, reminders and commands
	ChatWatch = "watch" // messages are stored for members who already have a key
)

type Chat struct {
	ID        int64
	Title     string
	Status    string
	Kind      string
	Reminders bool
}

func (c Chat) Main() bool { return c.Status == ChatAllowed && c.Kind == ChatMain }

// TouchChat records a chat the bot has seen. A new chat starts as status and
// created reports whether it was new; the title is kept up to date.
func (s *Store) TouchChat(ctx context.Context, id int64, title, status string) (Chat, bool, error) {
	var c Chat
	var created bool
	err := s.db.QueryRow(ctx, `insert into tg_chats (chat_id, title, status) values ($1, $2, $3)
		on conflict (chat_id) do update set title = case when excluded.title <> '' then excluded.title else tg_chats.title end
		returning chat_id, title, status, kind, reminders, (xmax = 0)`, id, title, status).
		Scan(&c.ID, &c.Title, &c.Status, &c.Kind, &c.Reminders, &created)
	return c, created, err
}

func (s *Store) ListChats(ctx context.Context) ([]Chat, error) {
	rows, err := s.db.Query(ctx, `select chat_id, title, status, kind, reminders from tg_chats order by created_at`)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row pgx.Row) (Chat, error) {
		var c Chat
		err := row.Scan(&c.ID, &c.Title, &c.Status, &c.Kind, &c.Reminders)
		return c, err
	})
}

func (s *Store) GetChat(ctx context.Context, id int64) (Chat, error) {
	var c Chat
	err := s.db.QueryRow(ctx, `select chat_id, title, status, kind, reminders from tg_chats where chat_id = $1`, id).
		Scan(&c.ID, &c.Title, &c.Status, &c.Kind, &c.Reminders)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// SetChatStatus changes a chat's status; denying a chat also deletes its stored messages.
func (s *Store) SetChatStatus(ctx context.Context, id int64, status string) error {
	if err := expectOne(s.db.Exec(ctx, `update tg_chats set status = $2, updated_at = now() where chat_id = $1`, id, status)); err != nil {
		return err
	}
	if status == ChatDenied {
		_, err := s.db.Exec(ctx, `delete from tg_messages where chat_id = $1`, id)
		return err
	}
	return nil
}

// AllowChat allows a chat as main or watch.
func (s *Store) AllowChat(ctx context.Context, id int64, kind string) error {
	return expectOne(s.db.Exec(ctx, `update tg_chats set status = 'allowed', kind = $2, updated_at = now() where chat_id = $1`, id, kind))
}

func (s *Store) SetChatReminders(ctx context.Context, id int64, on bool) error {
	return expectOne(s.db.Exec(ctx, `update tg_chats set reminders = $2, updated_at = now() where chat_id = $1`, id, on))
}

// MigrateChat moves a group that became a supergroup to its new id, keeping
// its status, settings and stored messages.
func (s *Store) MigrateChat(ctx context.Context, from, to int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `insert into tg_chats (chat_id, title, status, kind, reminders)
		select $2, title, status, kind, reminders from tg_chats where chat_id = $1
		on conflict (chat_id) do update set status = excluded.status, kind = excluded.kind, reminders = excluded.reminders, title = excluded.title`, from, to); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update tg_messages m set chat_id = $2 where chat_id = $1
		and not exists (select 1 from tg_messages x where x.chat_id = $2 and x.message_id = m.message_id)`, from, to); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from tg_chats where chat_id = $1`, from); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- api keys ----

const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	keyPrefix  = "e2_"

	NoExpiry       = time.Duration(0) // the default: keys work until revoked
	MaxKeysPerUser = 5                // older ones are revoked when a new key is issued
)

type APIKey struct {
	ID         int64
	TGUserID   *int64
	Name       string
	Scope      string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
}

const keyCols = `id, tg_user_id, name, scope, created_at, expires_at, last_used_at`

func scanKey(row pgx.Row) (APIKey, error) {
	var k APIKey
	err := row.Scan(&k.ID, &k.TGUserID, &k.Name, &k.Scope, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt)
	return k, err
}

// activeKey is the SQL condition for a usable key.
const activeKey = `revoked_at is null and (expires_at is null or expires_at > now())`

func (k APIKey) CanWrite() bool { return k.Scope == ScopeWrite }

func hashKey(key string) []byte {
	h := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return h[:]
}

func newKey() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return keyPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// CreateKey issues a new key valid for ttl (NoExpiry: until revoked) and
// returns it in plain text.
// Keys of Telegram users are also stored as is, so UserKey can return them
// again; console keys are shown once and kept only as a hash. A Telegram user
// may hold up to MaxKeysPerUser active keys; the oldest beyond that are revoked.
func (s *Store) CreateKey(ctx context.Context, name string, tgUserID *int64, scope string, ttl time.Duration) (string, APIKey, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", APIKey{}, err
	}
	defer tx.Rollback(ctx)

	plain := newKey()
	var stored *string
	if tgUserID != nil {
		stored = &plain
	}
	k, err := scanKey(tx.QueryRow(ctx, `insert into api_keys (tg_user_id, name, key_hash, key_plain, scope, expires_at)
		values ($1, $2, $3, $4, $5, case when $6::bigint = 0 then null else now() + $6 * interval '1 second' end) returning `+keyCols,
		tgUserID, name, hashKey(plain), stored, scope, int64(ttl.Seconds())))
	if err != nil {
		return "", APIKey{}, err
	}
	if tgUserID != nil {
		if _, err := tx.Exec(ctx, `update api_keys set revoked_at = now() where id in (
				select id from api_keys where tg_user_id = $1 and `+activeKey+`
				order by created_at desc, id desc offset $2)`, *tgUserID, MaxKeysPerUser); err != nil {
			return "", APIKey{}, err
		}
	}
	return plain, k, tx.Commit(ctx)
}

// UserKey returns the user's newest active stored key, or ErrNotFound when
// there is none (e.g. only keys issued before keys were stored).
func (s *Store) UserKey(ctx context.Context, tgUserID int64) (string, APIKey, error) {
	var plain string
	var k APIKey
	err := s.db.QueryRow(ctx, `select key_plain, `+keyCols+` from api_keys
		where tg_user_id = $1 and key_plain is not null and `+activeKey+`
		order by created_at desc, id desc limit 1`, tgUserID).
		Scan(&plain, &k.ID, &k.TGUserID, &k.Name, &k.Scope, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", k, ErrNotFound
	}
	return plain, k, err
}

func (s *Store) KeyByToken(ctx context.Context, token string) (APIKey, error) {
	if !strings.HasPrefix(strings.TrimSpace(token), keyPrefix) {
		return APIKey{}, ErrNotFound
	}
	k, err := scanKey(s.db.QueryRow(ctx, `select `+keyCols+` from api_keys where key_hash = $1 and `+activeKey, hashKey(token)))
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	return k, err
}

func (s *Store) ListKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.Query(ctx, `select `+keyCols+` from api_keys where `+activeKey+` order by name, id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanKey)
}

// CountUserKeys returns how many active keys the Telegram user holds.
func (s *Store) CountUserKeys(ctx context.Context, tgUserID int64) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `select count(*) from api_keys where tg_user_id = $1 and `+activeKey, tgUserID).Scan(&n)
	return n, err
}

func (s *Store) RevokeKey(ctx context.Context, id int64) error {
	return expectOne(s.db.Exec(ctx, `update api_keys set revoked_at = now() where id = $1 and revoked_at is null`, id))
}

func (s *Store) RevokeUserKeys(ctx context.Context, tgUserID int64) (int64, error) {
	tag, err := s.db.Exec(ctx, `update api_keys set revoked_at = now() where tg_user_id = $1 and revoked_at is null`, tgUserID)
	return tag.RowsAffected(), err
}

// LogAccess records a tool call and bumps last_used_at.
func (s *Store) LogAccess(ctx context.Context, keyID int64, tool string) error {
	if _, err := s.db.Exec(ctx, `insert into mcp_access (key_id, tool) values ($1, $2)`, keyID, tool); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `update api_keys set last_used_at = now() where id = $1`, keyID)
	return err
}

// ---- writers ----

func (s *Store) IsWriter(ctx context.Context, tgUserID int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `select exists(select 1 from tg_writers where tg_user_id = $1)`, tgUserID).Scan(&ok)
	return ok, err
}

// SetWriter grants or removes write access for a Telegram user and updates
// the scope of their active keys accordingly.
func (s *Store) SetWriter(ctx context.Context, tgUserID int64, name string, on bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	scope := ScopeRead
	if on {
		scope = ScopeWrite
		_, err = tx.Exec(ctx, `insert into tg_writers (tg_user_id, name) values ($1, $2)
			on conflict (tg_user_id) do update set name = excluded.name`, tgUserID, name)
	} else {
		_, err = tx.Exec(ctx, `delete from tg_writers where tg_user_id = $1`, tgUserID)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update api_keys set scope = $2 where tg_user_id = $1 and revoked_at is null`, tgUserID, scope); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) GetKey(ctx context.Context, id int64) (APIKey, error) {
	k, err := scanKey(s.db.QueryRow(ctx, `select `+keyCols+` from api_keys where id = $1 and `+activeKey, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	return k, err
}

// ---- kv ----

func (s *Store) GetKV(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRow(ctx, `select value from kv where key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetKV(ctx context.Context, key, value string) error {
	_, err := s.db.Exec(ctx, `insert into kv (key, value) values ($1, $2)
		on conflict (key) do update set value = excluded.value, updated_at = now()`, key, value)
	return err
}

// ---- single assignment (for partial updates) ----

func (s *Store) GetAssignment(ctx context.Context, id int64) (Assignment, error) {
	a, err := scanAssignment(s.db.QueryRow(ctx, `select `+assignmentCols+` from assignments where id = $1 and deleted_at is null`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Assignment{}, ErrNotFound
	}
	return a, err
}
