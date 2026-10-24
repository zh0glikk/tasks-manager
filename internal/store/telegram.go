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

func (s *Store) ListMessages(ctx context.Context, chatID int64, since, until time.Time, limit int) ([]ChatMessage, error) {
	// newest `limit` messages in the window, returned oldest first
	rows, err := s.db.Query(ctx, `select chat_id, message_id, coalesce(from_id, 0), from_name, text, reply_to, sent_at, edited_at from (
			select * from tg_messages where chat_id = $1 and sent_at >= $2 and sent_at < $3
			order by sent_at desc, message_id desc limit $4
		) m order by sent_at, message_id`, chatID, since, until, limit)
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
