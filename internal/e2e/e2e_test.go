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
	members map[int64]map[int64]string // chat id → user id → status
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
		status, ok := f.members[int64(p["chat_id"].(float64))][int64(p["user_id"].(float64))]
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

func (f *fakeTelegram) setMember(chat, id int64, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.members[chat] == nil {
		f.members[chat] = map[int64]string{}
	}
	f.members[chat][id] = status
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

	fake := &fakeTelegram{members: map[int64]map[int64]string{
		groupID: {irynaID: "member", maksymID: "administrator", adminID: "creator"},
	}}
	tg := httptest.NewServer(fake)
	t.Cleanup(tg.Close)

	loc, _ := time.LoadLocation("Europe/Kyiv")
	bot := telegram.New(telegram.Config{
		Token: "test", APIURL: tg.URL, ChatID: groupID, Admins: []int64{adminID},
		PublicURL: "https://e2.example", Retention: 14 * 24 * time.Hour, Location: loc,
	}, st)
	if err := bot.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(mcpserver.Handler(mcpserver.Deps{Store: st, Members: bot, Location: loc}))
	t.Cleanup(srv.Close)
	return st, bot, fake, srv.URL
}

func groupMsg(id int64, from int64, name, text string) telegram.Update {
	return chatMsg(groupID, "Е2 Аспірантура", id, from, name, text)
}

func chatMsg(chat int64, title string, id int64, from int64, name, text string) telegram.Update {
	return telegram.Update{UpdateID: id, Message: &telegram.Message{
		MessageID: id, From: &telegram.User{ID: from, FirstName: name},
		Chat: telegram.Chat{ID: chat, Type: "supergroup", Title: title}, Date: time.Now().Unix(), Text: text,
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
	if len(tools.Tools) != 11 {
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
	fake.setMember(groupID, irynaID, "left")
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
	fake.setMember(groupID, maksymID, "kicked")
	bot2 := telegram.New(telegram.Config{Token: "test", APIURL: strings.TrimSuffix(fakeURL(t, fake), "/"), ChatID: groupID}, st)
	if err := bot2.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
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

func TestMultipleChats(t *testing.T) {
	st, bot, fake, url := setup(t)
	ctx := context.Background()
	const englishID, basicID, migratedID = int64(-1005555), int64(-777), int64(-1007777)

	// a new chat waits for an admin; its messages aren't stored meanwhile
	bot.Handle(ctx, chatMsg(englishID, "Англійська гр.5", 1, maksymID, "Максим", "секретне до підтвердження"))
	if got := fake.last(t, adminID); !strings.Contains(got, "/allow -1005555") || !strings.Contains(got, "Англійська гр.5") {
		t.Fatalf("admin not asked: %q", got)
	}
	bot.Handle(ctx, chatMsg(englishID, "Англійська гр.5", 2, maksymID, "Максим", "/today"))
	for _, m := range fake.sent {
		if m.ChatID == englishID {
			t.Fatalf("bot answered in a pending chat: %q", m.Text)
		}
	}
	// a non-admin can't approve
	bot.Handle(ctx, dm(irynaID, "Ірина", "/allow -1005555"))
	if c, _ := st.GetChat(ctx, englishID); c.Status != store.ChatPending {
		t.Fatalf("non-admin changed status to %s", c.Status)
	}
	bot.Handle(ctx, dm(adminID, "Адмін", "/allow -1005555"))
	if got := fake.last(t, adminID); !strings.Contains(got, "дозволено") {
		t.Fatalf("/allow: %q", got)
	}
	bot.Handle(ctx, chatMsg(englishID, "Англійська гр.5", 3, maksymID, "Максим", "Unit 4 до четверга"))
	bot.Handle(ctx, groupMsg(4, irynaID, "Ірина", "привіт з основного чату"))

	// Ірина is only in the main chat, Максим is in both, Олег only in the English chat
	const olehID = int64(444)
	fake.setMember(englishID, maksymID, "member")
	fake.setMember(englishID, olehID, "member")

	bot.Handle(ctx, dm(olehID, "Олег", "/key"))
	olehKey := keyRe.FindString(fake.last(t, olehID))
	if olehKey == "" {
		t.Fatalf("member of the second chat got no key: %q", fake.last(t, olehID))
	}
	bot.Handle(ctx, dm(irynaID, "Ірина", "/key"))
	irynaKey := keyRe.FindString(fake.last(t, irynaID))
	bot.Handle(ctx, dm(maksymID, "Максим", "/key"))
	maksymKey := keyRe.FindString(fake.last(t, maksymID))

	read := func(key string, args map[string]any) string {
		s, err := connect(t, url, key)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		out, isErr := call(t, s, "chat_messages", args)
		if isErr {
			t.Fatalf("chat_messages: %q", out)
		}
		return out
	}
	if out := read(olehKey, nil); !strings.Contains(out, "Unit 4") || strings.Contains(out, "основного чату") || strings.Contains(out, "секретне") {
		t.Fatalf("Олег sees: %q", out)
	}
	if out := read(irynaKey, nil); strings.Contains(out, "Unit 4") || !strings.Contains(out, "основного чату") {
		t.Fatalf("Ірина sees: %q", out)
	}
	out := read(maksymKey, nil)
	if !strings.Contains(out, "[Англійська гр.5] Максим: Unit 4") || !strings.Contains(out, "[Е2 Аспірантура] Ірина") {
		t.Fatalf("Максим sees: %q", out)
	}
	if out := read(maksymKey, map[string]any{"chat": "англ"}); strings.Contains(out, "основного чату") || !strings.Contains(out, "Unit 4") {
		t.Fatalf("chat filter: %q", out)
	}

	// leaving one chat keeps the key while the user is still in another
	fake.setMember(groupID, maksymID, "left")
	left := groupMsg(5, maksymID, "Максим", "")
	left.Message.LeftChatMember = &telegram.User{ID: maksymID}
	bot.Handle(ctx, left)
	if s, err := connect(t, url, maksymKey); err != nil {
		t.Fatal("key revoked though still in the English chat")
	} else {
		s.Close()
	}

	// denying a chat deletes its history and hides it
	bot.Handle(ctx, dm(adminID, "Адмін", "/deny -1005555"))
	if s, err := connect(t, url, olehKey); err == nil {
		s.Close()
		t.Fatal("Олег still connects after his only chat was denied")
	}
	msgs, _ := st.ListMessages(ctx, []int64{englishID}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 100)
	if len(msgs) != 0 {
		t.Fatalf("denied chat history kept: %d messages", len(msgs))
	}

	// a basic group upgraded to a supergroup keeps its status and history
	bot.Handle(ctx, dm(adminID, "Адмін", "/chatid"))
	bot.Handle(ctx, telegram.Update{Message: &telegram.Message{MessageID: 1, From: &telegram.User{ID: irynaID, FirstName: "Ірина"},
		Chat: telegram.Chat{ID: basicID, Type: "group", Title: "Тест"}, Date: time.Now().Unix(), Text: "до міграції"}})
	bot.Handle(ctx, dm(adminID, "Адмін", "/allow -777"))
	bot.Handle(ctx, telegram.Update{Message: &telegram.Message{MessageID: 2, From: &telegram.User{ID: irynaID, FirstName: "Ірина"},
		Chat: telegram.Chat{ID: basicID, Type: "group", Title: "Тест"}, Date: time.Now().Unix(), Text: "збережено"}})
	bot.Handle(ctx, telegram.Update{Message: &telegram.Message{MessageID: 3, Chat: telegram.Chat{ID: basicID, Type: "group"},
		Date: time.Now().Unix(), MigrateToChat: migratedID}})
	c, err := st.GetChat(ctx, migratedID)
	if err != nil || c.Status != store.ChatAllowed {
		t.Fatalf("migrated chat: %+v %v", c, err)
	}
	msgs, _ = st.ListMessages(ctx, []int64{migratedID}, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 100)
	if len(msgs) != 1 || msgs[0].Text != "збережено" {
		t.Fatalf("history not moved: %+v", msgs)
	}

	bot.Handle(ctx, dm(adminID, "Адмін", "/chats"))
	if got := fake.last(t, adminID); !strings.Contains(got, "-1007777") || !strings.Contains(got, "заборонено") {
		t.Fatalf("/chats: %q", got)
	}
}
