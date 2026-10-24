// Package e2e exercises the Telegram bot and the MCP server together against a
// real Postgres (TEST_DATABASE_URL) and a fake Telegram Bot API.
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zh0glikk/tasks-manager/internal/db"
	"github.com/zh0glikk/tasks-manager/internal/mcpserver"
	"github.com/zh0glikk/tasks-manager/internal/store"
	"github.com/zh0glikk/tasks-manager/internal/telegram"
)

const (
	groupID  = int64(-1001234)
	irynaID  = int64(111)
	maksymID = int64(222)
	adminID  = int64(999)
	outsider = int64(333)
)

// fakeTelegram records sent messages and answers getChatMember from members.
type fakeTelegram struct {
	mu      sync.Mutex
	members map[int64]string // user id → status
	sent    []sent
}

type sent struct {
	ChatID int64
	Text   string
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var p map[string]any
	json.NewDecoder(r.Body).Decode(&p)
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "sendMessage":
		f.sent = append(f.sent, sent{ChatID: int64(p["chat_id"].(float64)), Text: p["text"].(string)})
		w.Write([]byte(`{"ok":true,"result":{}}`))
	case "getChatMember":
		status, ok := f.members[int64(p["user_id"].(float64))]
		if !ok {
			w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: user not found"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"status": status}})
	default:
		w.Write([]byte(`{"ok":true,"result":true}`))
	}
}

func (f *fakeTelegram) last(t *testing.T, chatID int64) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.sent) - 1; i >= 0; i-- {
		if f.sent[i].ChatID == chatID {
			return f.sent[i].Text
		}
	}
	t.Fatalf("no message sent to %d", chatID)
	return ""
}

func (f *fakeTelegram) setMember(id int64, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.members[id] = status
}

func setup(t *testing.T) (*store.Store, *telegram.Bot, *fakeTelegram, string) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `drop schema public cascade; create schema public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)

	fake := &fakeTelegram{members: map[int64]string{irynaID: "member", maksymID: "administrator", adminID: "creator"}}
	tg := httptest.NewServer(fake)
	t.Cleanup(tg.Close)

	loc, _ := time.LoadLocation("Europe/Kyiv")
	bot := telegram.New(telegram.Config{
		Token: "test", APIURL: tg.URL, ChatID: groupID, Admins: []int64{adminID},
		PublicURL: "https://e2.example", Retention: 14 * 24 * time.Hour, Location: loc,
	}, st)

	srv := httptest.NewServer(mcpserver.Handler(mcpserver.Deps{Store: st, Members: bot, Location: loc}))
	t.Cleanup(srv.Close)
	return st, bot, fake, srv.URL
}

func groupMsg(id int64, from int64, name, text string) telegram.Update {
	return telegram.Update{UpdateID: id, Message: &telegram.Message{
		MessageID: id, From: &telegram.User{ID: from, FirstName: name},
		Chat: telegram.Chat{ID: groupID, Type: "supergroup"}, Date: time.Now().Unix(), Text: text,
	}}
}

func dm(from int64, name, text string) telegram.Update {
	return telegram.Update{Message: &telegram.Message{
		MessageID: 1, From: &telegram.User{ID: from, FirstName: name},
		Chat: telegram.Chat{ID: from, Type: "private"}, Date: time.Now().Unix(), Text: text,
	}}
}

var keyRe = regexp.MustCompile(`e2_[a-z2-7]{32}`)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, url, key string) (*mcp.ClientSession, error) {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "1"}, nil)
	return c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearer{key}}, MaxRetries: -1,
	}, nil)
}

func call(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

func TestBotAndMCP(t *testing.T) {
	st, bot, fake, url := setup(t)
	ctx := context.Background()

	// chat messages are stored, edits replace the text, bot messages are ignored
	bot.Handle(ctx, groupMsg(10, irynaID, "Ірина", "Есе з англійської до вівторка 17:00, 250 слів"))
	bot.Handle(ctx, groupMsg(11, maksymID, "Максим", "ок"))
	edit := groupMsg(11, maksymID, "Максим", "ок, дякую")
	edit.Message, edit.EditedMessage = nil, edit.Message
	edit.EditedMessage.EditDate = time.Now().Unix()
	bot.Handle(ctx, edit)
	botMsg := groupMsg(12, 777, "Bot", "я бот")
	botMsg.Message.From.IsBot = true
	bot.Handle(ctx, botMsg)

	// /key: members get a key, outsiders don't
	bot.Handle(ctx, dm(outsider, "Чужий", "/key"))
	if got := fake.last(t, outsider); !strings.Contains(got, "тільки учасникам") || keyRe.MatchString(got) {
		t.Fatalf("outsider got: %q", got)
	}
	bot.Handle(ctx, dm(irynaID, "Ірина", "/key"))
	key := keyRe.FindString(fake.last(t, irynaID))
	if key == "" {
		t.Fatalf("no key in reply: %q", fake.last(t, irynaID))
	}
	if !strings.Contains(fake.last(t, irynaID), "https://e2.example/mcp") {
		t.Fatalf("reply lacks connect command: %q", fake.last(t, irynaID))
	}

	// MCP rejects missing and wrong keys
	if _, err := connect(t, url, ""); err == nil {
		t.Fatal("connected without a key")
	}
	if _, err := connect(t, url, "e2_"+strings.Repeat("a", 32)); err == nil {
		t.Fatal("connected with a wrong key")
	}

	s, err := connect(t, url, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	tools, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 10 {
		t.Fatalf("got %d tools", len(tools.Tools))
	}

	out, isErr := call(t, s, "chat_messages", nil)
	if isErr || !strings.Contains(out, "Ірина: Есе з англійської") || !strings.Contains(out, "Максим (змінено): ок, дякую") {
		t.Fatalf("chat_messages: %q", out)
	}
	if strings.Contains(out, "я бот") {
		t.Fatalf("bot message stored: %q", out)
	}

	out, _ = call(t, s, "courses", nil)
	if !strings.Contains(out, "course_id=2 Англійська") {
		t.Fatalf("courses: %q", out)
	}

	due := time.Now().AddDate(0, 0, 4).Format(time.DateOnly)
	args := map[string]any{"title": "Есе «My research»", "due_date": due, "due_time": "17:00", "course_id": 2}
	if out, isErr = call(t, s, "add_deadline", args); isErr || !strings.Contains(out, "Додано дедлайн") {
		t.Fatalf("add_deadline: %q", out)
	}
	if out, _ = call(t, s, "add_deadline", args); !strings.Contains(out, "схожий дедлайн") {
		t.Fatalf("duplicate not caught: %q", out)
	}
	if out, isErr = call(t, s, "add_deadline", map[string]any{"title": "x", "due_date": "2026-02-30"}); !isErr {
		t.Fatalf("bad date accepted: %q", out)
	}
	if out, isErr = call(t, s, "add_deadline", map[string]any{"title": "x", "due_date": due, "course_id": 99}); !isErr {
		t.Fatalf("unknown course accepted: %q", out)
	}

	out, _ = call(t, s, "upcoming_deadlines", nil)
	if !strings.Contains(out, "Есе «My research» (Англійська)") {
		t.Fatalf("upcoming: %q", out)
	}
	changes, _ := st.ListChanges(ctx, 5)
	if len(changes) == 0 || changes[0].Author != "Ірина (через Claude)" {
		t.Fatalf("change log author: %+v", changes)
	}

	if out, isErr = call(t, s, "add_news", map[string]any{"title": "Консультація 14.11 о 11:30", "course_id": 2}); isErr {
		t.Fatalf("add_news: %q", out)
	}
	if out, _ = call(t, s, "news", nil); !strings.Contains(out, "Консультація 14.11") {
		t.Fatalf("news: %q", out)
	}

	// read-only key can read but not write
	roKey, _, err := st.CreateKey(ctx, "Гість", nil, store.ScopeRead)
	if err != nil {
		t.Fatal(err)
	}
	ro, err := connect(t, url, roKey)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, isErr = call(t, ro, "calendar", nil); isErr {
		t.Fatal("read-only key can't read")
	}
	if out, isErr = call(t, ro, "add_news", map[string]any{"title": "spam"}); !isErr || !strings.Contains(out, "тільки читання") {
		t.Fatalf("read-only key wrote: %q", out)
	}

	// group commands and reminder
	bot.Handle(ctx, groupMsg(13, maksymID, "Максим", "/week"))
	if got := fake.last(t, groupID); !strings.Contains(got, "Есе «My research»") {
		t.Fatalf("/week: %q", got)
	}
	tomorrow := time.Now().AddDate(0, 0, 1).Format(time.DateOnly)
	if _, isErr = call(t, s, "add_deadline", map[string]any{"title": "Анотація теми", "due_date": tomorrow, "due_time": "12:00"}); isErr {
		t.Fatal("add tomorrow deadline")
	}
	if r := bot.Reminder(ctx); !strings.Contains(r, "Анотація теми") || !strings.Contains(r, "до 12:00") {
		t.Fatalf("reminder: %q", r)
	}

	// admin sees keys
	bot.Handle(ctx, dm(adminID, "Адмін", "/keys"))
	if got := fake.last(t, adminID); !strings.Contains(got, "Ірина") || !strings.Contains(got, "Гість") {
		t.Fatalf("/keys: %q", got)
	}

	// a new /key revokes the previous one
	bot.Handle(ctx, dm(irynaID, "Ірина", "/key"))
	newKey := keyRe.FindString(fake.last(t, irynaID))
	if newKey == "" || newKey == key {
		t.Fatal("no fresh key")
	}
	if _, err := connect(t, url, key); err == nil {
		t.Fatal("old key still works")
	}

	// leaving the group revokes the key
	s2, err := connect(t, url, newKey)
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
	fake.setMember(irynaID, "left")
	left := groupMsg(14, irynaID, "Ірина", "")
	left.Message.LeftChatMember = &telegram.User{ID: irynaID, FirstName: "Ірина"}
	bot.Handle(ctx, left)
	if _, err := connect(t, url, newKey); err == nil {
		t.Fatal("key works after leaving the group")
	}
}

func TestMemberCheckRevokesWithoutLeaveEvent(t *testing.T) {
	st, bot, fake, _ := setup(t)
	ctx := context.Background()

	bot.Handle(ctx, dm(maksymID, "Максим", "/key"))
	key := keyRe.FindString(fake.last(t, maksymID))
	// the bot missed the leave event; a fresh membership check must catch it
	fake.setMember(maksymID, "kicked")
	bot2 := telegram.New(telegram.Config{Token: "test", APIURL: strings.TrimSuffix(fakeURL(t, fake), "/"), ChatID: groupID}, st)
	srv := httptest.NewServer(mcpserver.Handler(mcpserver.Deps{Store: st, Members: bot2}))
	defer srv.Close()
	if _, err := connect(t, srv.URL, key); err == nil {
		t.Fatal("kicked member's key still works")
	}
	if keys, _ := st.ListKeys(ctx); len(keys) != 0 {
		t.Fatalf("key not revoked: %+v", keys)
	}
}

func fakeURL(t *testing.T, f *fakeTelegram) string {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv.URL
}
