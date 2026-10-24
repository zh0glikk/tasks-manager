// Package telegram runs the group bot: it stores chat messages for the MCP
// server, hands out personal MCP keys to group members and posts the agenda.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zh0glikk/tasks-manager/internal/store"
)

const (
	pollTimeout    = 50 // seconds, long polling
	memberCacheTTL = 10 * time.Minute
	reminderHour   = 20
	maxMessageLen  = 4000
)

type Config struct {
	Token     string
	APIURL    string // override for tests
	ChatID    int64  // optional chat that is allowed from the start; others are approved by an admin
	Admins    []int64
	PublicURL string
	Retention time.Duration
	Location  *time.Location
}

type Bot struct {
	cfg    Config
	tg     *Client
	store  *store.Store
	admins map[int64]bool

	mu      sync.Mutex
	members map[memberKey]memberEntry
}

type memberKey struct{ chat, user int64 }

type memberEntry struct {
	ok bool
	at time.Time
}

func New(cfg Config, s *store.Store) *Bot {
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	admins := map[int64]bool{}
	for _, id := range cfg.Admins {
		admins[id] = true
	}
	return &Bot{
		cfg:     cfg,
		tg:      NewClient(cfg.Token, cfg.APIURL),
		store:   s,
		admins:  admins,
		members: map[memberKey]memberEntry{},
	}
}

// Run polls Telegram for updates until ctx is done.
func (b *Bot) Run(ctx context.Context) {
	if err := b.Bootstrap(ctx); err != nil {
		log.Printf("telegram: bootstrap: %v", err)
	}
	if err := b.tg.DeleteWebhook(ctx); err != nil {
		log.Printf("telegram: deleteWebhook: %v", err)
	}
	go b.housekeeping(ctx)

	offset := b.loadOffset(ctx)
	backoff := time.Second
	for ctx.Err() == nil {
		updates, err := b.tg.GetUpdates(ctx, offset, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// 409 happens briefly during deploys while the old instance still polls
			log.Printf("telegram: getUpdates: %v (retrying in %s)", err, backoff)
			sleep(ctx, backoff)
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		for _, u := range updates {
			b.safeHandle(ctx, u)
			offset = u.UpdateID + 1
		}
		if len(updates) > 0 {
			if err := b.store.SetKV(ctx, "tg_offset", strconv.FormatInt(offset, 10)); err != nil {
				log.Printf("telegram: save offset: %v", err)
			}
		}
	}
}

// Bootstrap marks the configured chat (if any) as allowed.
func (b *Bot) Bootstrap(ctx context.Context) error {
	if b.cfg.ChatID == 0 {
		return nil
	}
	if _, _, err := b.store.TouchChat(ctx, b.cfg.ChatID, "", store.ChatAllowed); err != nil {
		return err
	}
	return b.store.SetChatStatus(ctx, b.cfg.ChatID, store.ChatAllowed)
}

func (b *Bot) loadOffset(ctx context.Context) int64 {
	v, err := b.store.GetKV(ctx, "tg_offset")
	if err != nil {
		log.Printf("telegram: load offset: %v", err)
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (b *Bot) safeHandle(ctx context.Context, u Update) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("telegram: panic handling update %d: %v", u.UpdateID, r)
		}
	}()
	b.handle(ctx, u)
}

// Handle processes one update; exported for tests.
func (b *Bot) Handle(ctx context.Context, u Update) { b.handle(ctx, u) }

func (b *Bot) handle(ctx context.Context, u Update) {
	switch {
	case u.Message != nil:
		b.handleMessage(ctx, u.Message, false)
	case u.EditedMessage != nil:
		b.handleMessage(ctx, u.EditedMessage, true)
	case u.MyChatMember != nil:
		// the bot itself was added to (or removed from) a chat
		if s := u.MyChatMember.NewChatMember.Status; s == "member" || s == "administrator" {
			b.seeChat(ctx, u.MyChatMember.Chat)
		}
	case u.ChatMember != nil:
		if s := u.ChatMember.NewChatMember.Status; s == "left" || s == "kicked" {
			if c, err := b.store.GetChat(ctx, u.ChatMember.Chat.ID); err == nil && c.Status == store.ChatAllowed {
				b.memberLeft(ctx, c.ID, u.ChatMember.NewChatMember.User.ID)
			}
		}
	}
}

func isGroup(c Chat) bool { return c.Type == "group" || c.Type == "supergroup" }

// seeChat records a group the bot is in; a new one is announced to the admins.
func (b *Bot) seeChat(ctx context.Context, tc Chat) store.Chat {
	c, created, err := b.store.TouchChat(ctx, tc.ID, tc.Title, store.ChatPending)
	if err != nil {
		log.Printf("telegram: touch chat %d: %v", tc.ID, err)
		return store.Chat{ID: tc.ID, Status: store.ChatPending}
	}
	if created {
		log.Printf("telegram: new chat %d %q, waiting for an admin", tc.ID, tc.Title)
		b.notifyAdmins(ctx, fmt.Sprintf("Мене додали в чат «%s» (<code>%d</code>).\n\nДозволити зберігати його переписку і видавати ключі його учасникам?\n/allow %d — так\n/deny %d — ні",
			html.EscapeString(tc.Title), tc.ID, tc.ID, tc.ID))
	}
	return c
}

func (b *Bot) notifyAdmins(ctx context.Context, text string) {
	for id := range b.admins {
		b.reply(ctx, id, text)
	}
}

func (b *Bot) handleMessage(ctx context.Context, m *Message, edited bool) {
	cmd, args := command(m.Text)
	switch {
	case m.Chat.Type == "private":
		if !edited && m.From != nil {
			b.private(ctx, m, cmd, args)
		}
	case isGroup(m.Chat):
		if m.MigrateToChat != 0 {
			// the group became a supergroup and got a new id
			if err := b.store.MigrateChat(ctx, m.Chat.ID, m.MigrateToChat); err != nil {
				log.Printf("telegram: migrate chat %d → %d: %v", m.Chat.ID, m.MigrateToChat, err)
			} else {
				log.Printf("telegram: chat %d migrated to %d", m.Chat.ID, m.MigrateToChat)
			}
			return
		}
		c := b.seeChat(ctx, m.Chat)
		if c.Status != store.ChatAllowed {
			if cmd == "/chatid" {
				b.reply(ctx, m.Chat.ID, fmt.Sprintf("chat_id цього чату: <code>%d</code>. Чат ще не підтверджений адміном бота.", m.Chat.ID))
			}
			return
		}
		if m.LeftChatMember != nil {
			b.memberLeft(ctx, c.ID, m.LeftChatMember.ID)
		}
		b.save(ctx, m)
		if !edited {
			b.group(ctx, m, cmd)
		}
	}
}

func (b *Bot) save(ctx context.Context, m *Message) {
	text := messageText(m)
	if text == "" || (m.From != nil && m.From.IsBot) {
		return
	}
	cm := store.ChatMessage{
		ChatID:    m.Chat.ID,
		MessageID: m.MessageID,
		FromName:  m.From.DisplayName(),
		Text:      text,
		SentAt:    time.Unix(m.Date, 0),
	}
	if m.From != nil {
		cm.FromID = m.From.ID
	}
	if m.ReplyTo != nil {
		id := m.ReplyTo.MessageID
		cm.ReplyTo = &id
	}
	if m.EditDate != 0 {
		t := time.Unix(m.EditDate, 0)
		cm.EditedAt = &t
	}
	if err := b.store.SaveMessage(ctx, cm); err != nil {
		log.Printf("telegram: save message: %v", err)
	}
}

func messageText(m *Message) string {
	t := strings.TrimSpace(m.Text)
	if t == "" {
		t = strings.TrimSpace(m.Caption)
	}
	var media string
	switch {
	case len(m.Photo) > 0:
		media = "[фото]"
	case len(m.Video) > 0:
		media = "[відео]"
	case len(m.Voice) > 0:
		media = "[голосове]"
	case m.Document != nil:
		media = "[файл: " + m.Document.FileName + "]"
	case m.Sticker != nil:
		media = "[стікер " + m.Sticker.Emoji + "]"
	case m.Poll != nil:
		media = "[опитування: " + m.Poll.Question + "]"
	}
	if media != "" && t != "" {
		return media + " " + t
	}
	return media + t
}

// ---- commands ----

func command(text string) (string, string) {
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	cmd, args, _ := strings.Cut(text, " ")
	cmd, _, _ = strings.Cut(cmd, "@") // /today@SomeBot
	return strings.ToLower(cmd), strings.TrimSpace(args)
}

func (b *Bot) group(ctx context.Context, m *Message, cmd string) {
	today := b.today()
	switch cmd {
	case "/today":
		b.reply(ctx, m.Chat.ID, b.agenda(ctx, today, today))
	case "/week":
		b.reply(ctx, m.Chat.ID, b.agenda(ctx, today, today.AddDate(0, 0, 6)))
	case "/help":
		b.reply(ctx, m.Chat.ID, "/today — що сьогодні\n/week — що на тиждень\n\nПерсональний ключ для Claude: напиши мені в особисті /key")
	case "/chatid":
		b.reply(ctx, m.Chat.ID, fmt.Sprintf("chat_id цього чату: <code>%d</code>", m.Chat.ID))
	}
}

const privateHelp = `Я бот календаря групи.

<b>У чаті групи:</b> /today, /week — що на сьогодні й на тиждень; увечері нагадую про завтрашні дедлайни.

<b>Тут, в особистих:</b>
/key — отримати персональний ключ для Claude (MCP): читати переписку групи, календар і додавати дедлайни
/key read — ключ тільки для читання
/revoke — відкликати свій ключ

Ключ видається тільки учасникам чатів групи і перестає працювати, якщо вийти з них. Я зберігаю повідомлення обмежений час, щоб Claude міг робити саммарі; через Claude кожен бачить тільки ті чати, де він сам є.`

func (b *Bot) private(ctx context.Context, m *Message, cmd, args string) {
	uid := m.From.ID
	switch cmd {
	case "/key":
		b.issueKey(ctx, m, args)
	case "/revoke":
		n, err := b.store.RevokeUserKeys(ctx, uid)
		if err != nil {
			log.Printf("telegram: revoke: %v", err)
			b.reply(ctx, uid, "Не вдалося відкликати ключ, спробуй пізніше.")
			return
		}
		b.reply(ctx, uid, fmt.Sprintf("Відкликано ключів: %d.", n))
	case "/chatid":
		b.reply(ctx, uid, fmt.Sprintf("Твій user_id: <code>%d</code>", uid))
	case "/keys":
		if b.admins[uid] {
			b.reply(ctx, uid, b.listKeys(ctx))
		}
	case "/chats":
		if b.admins[uid] {
			b.reply(ctx, uid, b.listChats(ctx))
		}
	case "/allow", "/deny":
		if b.admins[uid] {
			b.setChatStatus(ctx, uid, cmd, args)
		}
	case "/reminders":
		if b.admins[uid] {
			b.setReminders(ctx, uid, args)
		}
	case "/revokekey":
		if !b.admins[uid] {
			return
		}
		id, err := strconv.ParseInt(args, 10, 64)
		if err != nil {
			b.reply(ctx, uid, "Формат: /revokekey 12")
			return
		}
		if err := b.store.RevokeKey(ctx, id); err != nil {
			b.reply(ctx, uid, "Ключ не знайдено або вже відкликано.")
			return
		}
		b.reply(ctx, uid, fmt.Sprintf("Ключ #%d відкликано.", id))
	default:
		help := privateHelp
		if b.admins[uid] {
			help += "\n\n<b>Адмін:</b> /chats — чати бота, /allow ID, /deny ID, /reminders ID on|off, /keys, /revokekey ID"
		}
		b.reply(ctx, uid, help)
	}
}

func (b *Bot) issueKey(ctx context.Context, m *Message, args string) {
	uid := m.From.ID
	ok, err := b.IsMember(ctx, uid)
	if err != nil {
		log.Printf("telegram: member check %d: %v", uid, err)
		b.reply(ctx, uid, "Не вдалося перевірити членство в групі, спробуй пізніше.")
		return
	}
	if !ok {
		b.reply(ctx, uid, "Ключ видається тільки учасникам чатів групи, до яких підключений бот.")
		return
	}
	scope := store.ScopeWrite
	if strings.EqualFold(args, "read") {
		scope = store.ScopeRead
	}
	key, k, err := b.store.CreateKey(ctx, m.From.DisplayName(), &uid, scope)
	if err != nil {
		log.Printf("telegram: create key: %v", err)
		b.reply(ctx, uid, "Не вдалося створити ключ, спробуй пізніше.")
		return
	}
	url := strings.TrimRight(b.cfg.PublicURL, "/") + "/mcp"
	if b.cfg.PublicURL == "" {
		url = "https://АДРЕСА-САЙТУ/mcp"
	}
	access := "читання і запис"
	if !k.CanWrite() {
		access = "тільки читання"
	}
	b.reply(ctx, uid, fmt.Sprintf(`Твій ключ (%s):

<code>%s</code>

Підключити в Claude Code:
<code>claude mcp add --transport http e2 %s --header "Authorization: Bearer %s"</code>

Попередній ключ, якщо був, відкликано. Нікому не пересилай ключ і видали це повідомлення після копіювання. Новий ключ — /key, відкликати — /revoke.`,
		access, key, html.EscapeString(url), key))
}

func (b *Bot) listKeys(ctx context.Context) string {
	keys, err := b.store.ListKeys(ctx)
	if err != nil {
		return "Помилка: " + html.EscapeString(err.Error())
	}
	if len(keys) == 0 {
		return "Активних ключів немає."
	}
	var sb strings.Builder
	sb.WriteString("<b>Активні ключі</b>\n")
	for _, k := range keys {
		used := "ще не використовувався"
		if k.LastUsedAt != nil {
			used = "востаннє " + k.LastUsedAt.In(b.cfg.Location).Format("02.01 15:04")
		}
		fmt.Fprintf(&sb, "#%d %s · %s · %s\n", k.ID, html.EscapeString(k.Name), k.Scope, used)
	}
	sb.WriteString("\nВідкликати: /revokekey ID")
	return sb.String()
}

// ---- chats (admin) ----

func (b *Bot) listChats(ctx context.Context) string {
	chats, err := b.store.ListChats(ctx)
	if err != nil {
		return "Помилка: " + html.EscapeString(err.Error())
	}
	if len(chats) == 0 {
		return "Мене ще не додали в жоден чат."
	}
	labels := map[string]string{store.ChatAllowed: "дозволено", store.ChatPending: "чекає рішення", store.ChatDenied: "заборонено"}
	var sb strings.Builder
	sb.WriteString("<b>Чати</b>\n")
	for _, c := range chats {
		rem := ""
		if c.Status == store.ChatAllowed && !c.Reminders {
			rem = " · без нагадувань"
		}
		fmt.Fprintf(&sb, "<code>%d</code> %s · %s%s\n", c.ID, html.EscapeString(c.Title), labels[c.Status], rem)
	}
	sb.WriteString("\n/allow ID · /deny ID · /reminders ID on|off")
	return sb.String()
}

func (b *Bot) setChatStatus(ctx context.Context, uid int64, cmd, args string) {
	id, err := strconv.ParseInt(args, 10, 64)
	if err != nil {
		b.reply(ctx, uid, "Формат: "+cmd+" -1001234567890 (id — з /chats)")
		return
	}
	status := store.ChatAllowed
	if cmd == "/deny" {
		status = store.ChatDenied
	}
	if err := b.store.SetChatStatus(ctx, id, status); err != nil {
		b.reply(ctx, uid, "Чат не знайдено. Список — /chats")
		return
	}
	b.mu.Lock()
	for k := range b.members {
		if k.chat == id {
			delete(b.members, k)
		}
	}
	b.mu.Unlock()
	if status == store.ChatAllowed {
		b.reply(ctx, uid, fmt.Sprintf("Чат <code>%d</code> дозволено: зберігаю переписку, учасники можуть отримати ключ через /key.", id))
	} else {
		b.reply(ctx, uid, fmt.Sprintf("Чат <code>%d</code> заборонено, збережену переписку з нього видалено.", id))
	}
}

func (b *Bot) setReminders(ctx context.Context, uid int64, args string) {
	idStr, mode, _ := strings.Cut(args, " ")
	id, err := strconv.ParseInt(idStr, 10, 64)
	on := strings.TrimSpace(mode) == "on"
	if err != nil || (!on && strings.TrimSpace(mode) != "off") {
		b.reply(ctx, uid, "Формат: /reminders -1001234567890 on|off")
		return
	}
	if err := b.store.SetChatReminders(ctx, id, on); err != nil {
		b.reply(ctx, uid, "Чат не знайдено. Список — /chats")
		return
	}
	b.reply(ctx, uid, fmt.Sprintf("Нагадування в чаті <code>%d</code>: %s.", id, map[bool]string{true: "увімкнено", false: "вимкнено"}[on]))
}

func (b *Bot) allowedChats(ctx context.Context) ([]store.Chat, error) {
	chats, err := b.store.ListChats(ctx)
	if err != nil {
		return nil, err
	}
	out := chats[:0]
	for _, c := range chats {
		if c.Status == store.ChatAllowed {
			out = append(out, c)
		}
	}
	return out, nil
}

// ---- membership ----

// Enabled is always true for a running bot; it satisfies mcpserver.Members.
func (b *Bot) Enabled() bool { return true }

// IsMember reports whether the user is in at least one allowed chat.
func (b *Bot) IsMember(ctx context.Context, userID int64) (bool, error) {
	chats, err := b.MemberChats(ctx, userID)
	return len(chats) > 0, err
}

// MemberChats lists the allowed chats the user is currently in (cached per chat).
func (b *Bot) MemberChats(ctx context.Context, userID int64) ([]store.Chat, error) {
	chats, err := b.allowedChats(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.Chat
	for _, c := range chats {
		ok, err := b.memberOf(ctx, c.ID, userID)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (b *Bot) memberOf(ctx context.Context, chatID, userID int64) (bool, error) {
	key := memberKey{chatID, userID}
	b.mu.Lock()
	e, ok := b.members[key]
	b.mu.Unlock()
	if ok && time.Since(e.at) < memberCacheTTL {
		return e.ok, nil
	}

	m, err := b.tg.GetChatMember(ctx, chatID, userID)
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Code == 400:
		// user never joined / not found
		m = ChatMember{Status: "left"}
	case err != nil:
		return false, err
	}
	member := m.Status == "creator" || m.Status == "administrator" || m.Status == "member" ||
		(m.Status == "restricted" && m.IsMember)

	b.mu.Lock()
	b.members[key] = memberEntry{ok: member, at: time.Now()}
	b.mu.Unlock()
	return member, nil
}

// memberLeft revokes the user's keys once they are in none of the allowed chats.
func (b *Bot) memberLeft(ctx context.Context, chatID, userID int64) {
	b.mu.Lock()
	b.members[memberKey{chatID, userID}] = memberEntry{ok: false, at: time.Now()}
	b.mu.Unlock()
	if still, err := b.IsMember(ctx, userID); err != nil || still {
		return
	}
	if n, err := b.store.RevokeUserKeys(ctx, userID); err != nil {
		log.Printf("telegram: revoke keys of %d: %v", userID, err)
	} else if n > 0 {
		log.Printf("telegram: user %d left the group chats, revoked %d key(s)", userID, n)
	}
}

// ---- agenda, reminders, retention ----

var (
	weekdays = []string{"нд", "пн", "вт", "ср", "чт", "пт", "сб"}
	months   = []string{"січня", "лютого", "березня", "квітня", "травня", "червня", "липня", "серпня", "вересня", "жовтня", "листопада", "грудня"}
)

func dayLabel(d time.Time) string {
	return fmt.Sprintf("%s, %d %s", weekdays[d.Weekday()], d.Day(), months[d.Month()-1])
}

func (b *Bot) today() time.Time {
	now := time.Now().In(b.cfg.Location)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, b.cfg.Location)
}

func (b *Bot) agenda(ctx context.Context, from, to time.Time) string {
	f, t := from.Format(time.DateOnly), to.Format(time.DateOnly)
	courses, err1 := b.store.ListCourses(ctx)
	events, err2 := b.store.ListEvents(ctx, f, t)
	dls, err3 := b.store.ListAssignments(ctx, f, t)
	if err := errors.Join(err1, err2, err3); err != nil {
		log.Printf("telegram: agenda: %v", err)
		return "Не вдалося завантажити розклад."
	}
	short := map[int64]string{}
	for _, c := range courses {
		short[c.ID] = c.Short
	}
	courseName := func(id *int64) string {
		if id == nil || short[*id] == "" {
			return "Загальне"
		}
		return short[*id]
	}

	var sb strings.Builder
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		key := d.Format(time.DateOnly)
		var lines []string
		for _, a := range dls {
			if a.DueDate != key {
				continue
			}
			due := "дедлайн"
			if a.DueTime != "" {
				due = "дедлайн до " + a.DueTime
			}
			lines = append(lines, fmt.Sprintf("❗ %s: %s · %s", due, html.EscapeString(a.Title), html.EscapeString(courseName(a.CourseID))))
		}
		for _, e := range events {
			if e.Date != key {
				continue
			}
			tm := e.StartTime
			if e.EndTime != "" {
				tm += "–" + e.EndTime
			}
			lines = append(lines, fmt.Sprintf("%s %s · %s", tm, html.EscapeString(courseName(e.CourseID)), html.EscapeString(e.Kind)))
		}
		if len(lines) == 0 && from != to {
			continue
		}
		fmt.Fprintf(&sb, "<b>%s</b>\n", dayLabel(d))
		if len(lines) == 0 {
			sb.WriteString("нічого не заплановано\n")
		}
		for _, l := range lines {
			sb.WriteString(l + "\n")
		}
		sb.WriteString("\n")
	}
	if sb.Len() == 0 {
		return "На цей період нічого не заплановано."
	}
	return b.withLink(strings.TrimSpace(sb.String()))
}

func (b *Bot) withLink(s string) string {
	if b.cfg.PublicURL != "" {
		s += "\n\n" + html.EscapeString(b.cfg.PublicURL)
	}
	return s
}

func (b *Bot) housekeeping(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	var lastPurge time.Time
	for {
		if time.Since(lastPurge) > time.Hour && b.cfg.Retention > 0 {
			if n, err := b.store.Purge(ctx, time.Now().Add(-b.cfg.Retention)); err != nil {
				log.Printf("telegram: purge: %v", err)
			} else {
				lastPurge = time.Now()
				if n > 0 {
					log.Printf("telegram: purged %d old chat message(s)", n)
				}
			}
		}
		b.maybeRemind(ctx)

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// maybeRemind posts tomorrow's deadlines to the group once a day at reminderHour.
func (b *Bot) maybeRemind(ctx context.Context) {
	now := time.Now().In(b.cfg.Location)
	if now.Hour() != reminderHour {
		return
	}
	today := now.Format(time.DateOnly)
	if last, _ := b.store.GetKV(ctx, "reminder_date"); last == today {
		return
	}
	if err := b.store.SetKV(ctx, "reminder_date", today); err != nil {
		log.Printf("telegram: reminder: %v", err)
		return
	}
	text := b.Reminder(ctx)
	if text == "" {
		return
	}
	chats, err := b.allowedChats(ctx)
	if err != nil {
		log.Printf("telegram: reminder: %v", err)
		return
	}
	for _, c := range chats {
		if c.Reminders {
			b.reply(ctx, c.ID, text)
		}
	}
}

// Reminder returns the message about tomorrow's deadlines, or "" if none.
func (b *Bot) Reminder(ctx context.Context) string {
	tomorrow := b.today().AddDate(0, 0, 1)
	dls, err := b.store.ListAssignments(ctx, tomorrow.Format(time.DateOnly), tomorrow.Format(time.DateOnly))
	if err != nil || len(dls) == 0 {
		return ""
	}
	courses, _ := b.store.ListCourses(ctx)
	short := map[int64]string{}
	for _, c := range courses {
		short[c.ID] = c.Short
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>Нагадування: завтра, %s</b>\n", dayLabel(tomorrow))
	for _, a := range dls {
		line := "• " + html.EscapeString(a.Title)
		if a.CourseID != nil && short[*a.CourseID] != "" {
			line += " · " + html.EscapeString(short[*a.CourseID])
		}
		if a.DueTime != "" {
			line += ", до " + a.DueTime
		}
		sb.WriteString(line + "\n")
	}
	return b.withLink(strings.TrimSpace(sb.String()))
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if r := []rune(text); len(r) > maxMessageLen {
		text = string(r[:maxMessageLen]) + "…"
	}
	if err := b.tg.SendMessage(ctx, chatID, text); err != nil {
		log.Printf("telegram: sendMessage to %d: %v", chatID, err)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
