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
)

type Chat struct {
	ID        int64
	Title     string
	Status    string
	Reminders bool
}

// TouchChat records a chat the bot has seen. A new chat starts as status and
// created reports whether it was new; the title is kept up to date.
func (s *Store) TouchChat(ctx context.Context, id int64, title, status string) (Chat, bool, error) {
	var c Chat
	var created bool
	err := s.db.QueryRow(ctx, `insert into tg_chats (chat_id, title, status) values ($1, $2, $3)
		on conflict (chat_id) do update set title = case when excluded.title <> '' then excluded.title else tg_chats.title end
		returning chat_id, title, status, reminders, (xmax = 0)`, id, title, status).
		Scan(&c.ID, &c.Title, &c.Status, &c.Reminders, &created)
	return c, created, err
}

func (s *Store) ListChats(ctx context.Context) ([]Chat, error) {
	rows, err := s.db.Query(ctx, `select chat_id, title, status, reminders from tg_chats order by created_at`)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row pgx.Row) (Chat, error) {
		var c Chat
		err := row.Scan(&c.ID, &c.Title, &c.Status, &c.Reminders)
		return c, err
	})
}

func (s *Store) GetChat(ctx context.Context, id int64) (Chat, error) {
	var c Chat
	err := s.db.QueryRow(ctx, `select chat_id, title, status, reminders from tg_chats where chat_id = $1`, id).
		Scan(&c.ID, &c.Title, &c.Status, &c.Reminders)
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
	if _, err := tx.Exec(ctx, `insert into tg_chats (chat_id, title, status, reminders)
		select $2, title, status, reminders from tg_chats where chat_id = $1
		on conflict (chat_id) do update set status = excluded.status, reminders = excluded.reminders, title = excluded.title`, from, to); err != nil {
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
)

type APIKey struct {
	ID         int64
	TGUserID   *int64
	Name       string
	Scope      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

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

// CreateKey issues a new key and returns it in plain text; it is not stored.
// A Telegram user keeps a single active key: older ones are revoked.
func (s *Store) CreateKey(ctx context.Context, name string, tgUserID *int64, scope string) (string, APIKey, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", APIKey{}, err
	}
	defer tx.Rollback(ctx)

	if tgUserID != nil {
		if _, err := tx.Exec(ctx, `update api_keys set revoked_at = now() where tg_user_id = $1 and revoked_at is null`, *tgUserID); err != nil {
			return "", APIKey{}, err
		}
	}
	plain := newKey()
	k := APIKey{TGUserID: tgUserID, Name: name, Scope: scope}
	err = tx.QueryRow(ctx, `insert into api_keys (tg_user_id, name, key_hash, scope) values ($1, $2, $3, $4) returning id, created_at`,
		tgUserID, name, hashKey(plain), scope).Scan(&k.ID, &k.CreatedAt)
	if err != nil {
		return "", APIKey{}, err
	}
	return plain, k, tx.Commit(ctx)
}

func (s *Store) KeyByToken(ctx context.Context, token string) (APIKey, error) {
	if !strings.HasPrefix(strings.TrimSpace(token), keyPrefix) {
		return APIKey{}, ErrNotFound
	}
	var k APIKey
	err := s.db.QueryRow(ctx, `select id, tg_user_id, name, scope, created_at, last_used_at from api_keys
		where key_hash = $1 and revoked_at is null`, hashKey(token)).
		Scan(&k.ID, &k.TGUserID, &k.Name, &k.Scope, &k.CreatedAt, &k.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	return k, err
}

func (s *Store) ListKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.Query(ctx, `select id, tg_user_id, name, scope, created_at, last_used_at from api_keys
		where revoked_at is null order by id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row pgx.Row) (APIKey, error) {
		var k APIKey
		err := row.Scan(&k.ID, &k.TGUserID, &k.Name, &k.Scope, &k.CreatedAt, &k.LastUsedAt)
		return k, err
	})
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
