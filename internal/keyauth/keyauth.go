// Package keyauth verifies personal API keys issued by the Telegram bot; it is
// shared by the MCP server and the website API.
package keyauth

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/zh0glikk/tasks-manager/internal/store"
)

// ErrInvalid means the key is unknown, revoked or its owner left the group.
var ErrInvalid = errors.New("invalid key")

// Members checks group membership; implemented by the Telegram bot.
type Members interface {
	// IsMember reports whether the user is in at least one allowed main chat.
	IsMember(ctx context.Context, userID int64) (bool, error)
}

type Verifier struct {
	Store   *store.Store
	Members Members // nil when the bot isn't configured
}

// Verify resolves a token to an active key whose owner is still in the group.
// A key of a user who left is revoked on the spot.
func (v Verifier) Verify(ctx context.Context, token string) (store.APIKey, error) {
	k, err := v.Store.KeyByToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		return k, ErrInvalid
	}
	if err != nil {
		return k, err
	}
	if k.TGUserID != nil && v.Members != nil {
		ok, err := v.Members.IsMember(ctx, *k.TGUserID)
		if err != nil {
			return k, err
		}
		if !ok {
			if _, err := v.Store.RevokeUserKeys(ctx, *k.TGUserID); err != nil {
				log.Printf("keyauth: revoke keys of %d: %v", *k.TGUserID, err)
			}
			return k, ErrInvalid
		}
	}
	return k, nil
}

// Bearer extracts the token from an "Authorization: Bearer …" header.
func Bearer(r *http.Request) (string, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok = strings.TrimSpace(tok)
	return tok, ok && tok != ""
}
