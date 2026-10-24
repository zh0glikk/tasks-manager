package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const defaultAPIURL = "https://api.telegram.org"

// Client is a minimal Telegram Bot API client covering what the bot uses.
type Client struct {
	base string
	http *http.Client
}

func NewClient(token, apiURL string) *Client {
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	return &Client{
		base: strings.TrimRight(apiURL, "/") + "/bot" + token + "/",
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

type APIError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram: %d %s", e.Code, e.Description)
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	if !env.OK {
		return &APIError{Code: env.ErrorCode, Description: env.Description, RetryAfter: env.Parameters.RetryAfter}
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// ---- types (only the fields we read) ----

type Update struct {
	UpdateID      int64              `json:"update_id"`
	Message       *Message           `json:"message"`
	EditedMessage *Message           `json:"edited_message"`
	ChatMember    *ChatMemberUpdated `json:"chat_member"`
	MyChatMember  *ChatMemberUpdated `json:"my_chat_member"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

func (u *User) DisplayName() string {
	if u == nil {
		return ""
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		name = u.Username
	}
	return name
}

type Chat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type Message struct {
	MessageID      int64           `json:"message_id"`
	From           *User           `json:"from"`
	Chat           Chat            `json:"chat"`
	Date           int64           `json:"date"`
	EditDate       int64           `json:"edit_date"`
	Text           string          `json:"text"`
	Caption        string          `json:"caption"`
	ReplyTo        *Message        `json:"reply_to_message"`
	LeftChatMember *User           `json:"left_chat_member"`
	MigrateToChat  int64           `json:"migrate_to_chat_id"`
	NewChatMembers []User          `json:"new_chat_members"`
	Photo          json.RawMessage `json:"photo"`
	Video          json.RawMessage `json:"video"`
	Voice          json.RawMessage `json:"voice"`
	Sticker        *struct {
		Emoji string `json:"emoji"`
	} `json:"sticker"`
	Document *struct {
		FileName string `json:"file_name"`
	} `json:"document"`
	Poll *struct {
		Question string `json:"question"`
	} `json:"poll"`
}

type ChatMember struct {
	Status   string `json:"status"`
	IsMember bool   `json:"is_member"`
	User     User   `json:"user"`
}

type ChatMemberUpdated struct {
	Chat          Chat       `json:"chat"`
	NewChatMember ChatMember `json:"new_chat_member"`
}

// ---- methods ----

func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var out []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeout,
		"allowed_updates": []string{"message", "edited_message", "chat_member", "my_chat_member"},
	}, &out)
	return out, err
}

func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", map[string]any{}, nil)
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, html string) error {
	return c.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     html,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}, nil)
}

func (c *Client) GetChatMember(ctx context.Context, chatID, userID int64) (ChatMember, error) {
	var m ChatMember
	err := c.call(ctx, "getChatMember", map[string]any{"chat_id": chatID, "user_id": userID}, &m)
	return m, err
}
