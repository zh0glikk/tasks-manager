package telegram

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/zh0glikk/tasks-manager/internal/store"
)

// Export is a chat history exported from Telegram Desktop (result.json).
type Export struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	ID       int64           `json:"id"`
	Messages []exportMessage `json:"messages"`
}

type exportMessage struct {
	ID            int64           `json:"id"`
	Type          string          `json:"type"`
	DateUnix      string          `json:"date_unixtime"`
	From          string          `json:"from"`
	FromID        string          `json:"from_id"`
	Text          json.RawMessage `json:"text"`
	ReplyTo       int64           `json:"reply_to_message_id"`
	EditedUnix    string          `json:"edited_unixtime"`
	ForwardedFrom string          `json:"forwarded_from"`
	Photo         string          `json:"photo"`
	File          string          `json:"file"`
	FileName      string          `json:"file_name"`
	MediaType     string          `json:"media_type"`
	StickerEmoji  string          `json:"sticker_emoji"`
	Poll          *struct {
		Question string `json:"question"`
	} `json:"poll"`
}

func ParseExport(r io.Reader) (*Export, error) {
	var e Export
	if err := json.NewDecoder(r).Decode(&e); err != nil {
		return nil, err
	}
	if e.ID == 0 || e.Messages == nil {
		return nil, fmt.Errorf("not a Telegram chat export (result.json of a single chat)")
	}
	return &e, nil
}

// ChatID converts the export id to the Bot API chat id.
func (e *Export) ChatID() (int64, error) {
	switch e.Type {
	case "private_group", "public_group":
		return -e.ID, nil
	case "private_supergroup", "public_supergroup", "private_channel", "public_channel":
		return -(1_000_000_000_000 + e.ID), nil
	}
	return 0, fmt.Errorf("unsupported chat type %q", e.Type)
}

// ImportedID maps an exported message id into a range that can't collide with
// ids the bot sees live (in basic groups the two numberings differ).
func ImportedID(id int64) int64 { return -id }

// ChatMessages converts the export into stored messages (service messages and
// empty ones are skipped).
func (e *Export) ChatMessages(chatID int64) []store.ChatMessage {
	var out []store.ChatMessage
	for _, m := range e.Messages {
		if m.Type != "message" {
			continue
		}
		text := exportText(m)
		if text == "" {
			continue
		}
		cm := store.ChatMessage{
			ChatID:    chatID,
			MessageID: ImportedID(m.ID),
			FromName:  m.From,
			Text:      text,
			SentAt:    unix(m.DateUnix),
		}
		if id, err := strconv.ParseInt(strings.TrimPrefix(m.FromID, "user"), 10, 64); err == nil {
			cm.FromID = id
		}
		if m.ReplyTo != 0 {
			r := ImportedID(m.ReplyTo)
			cm.ReplyTo = &r
		}
		if m.EditedUnix != "" {
			t := unix(m.EditedUnix)
			cm.EditedAt = &t
		}
		out = append(out, cm)
	}
	return out
}

func exportText(m exportMessage) string {
	t := strings.TrimSpace(flattenText(m.Text))
	var media string
	switch {
	case m.Photo != "":
		media = "[фото]"
	case m.MediaType == "sticker":
		media = "[стікер " + m.StickerEmoji + "]"
	case m.MediaType == "animation":
		media = "[gif]"
	case m.MediaType == "voice_message":
		media = "[голосове]"
	case m.MediaType == "video_file" || m.MediaType == "video_message":
		media = "[відео]"
	case m.File != "" || m.FileName != "":
		media = "[файл: " + m.FileName + "]"
	case m.Poll != nil:
		media = "[опитування: " + m.Poll.Question + "]"
	}
	if media != "" && t != "" {
		t = media + " " + t
	} else if media != "" {
		t = media
	}
	if t != "" && m.ForwardedFrom != "" {
		t = "[переслано від " + m.ForwardedFrom + "] " + t
	}
	return t
}

// flattenText handles both forms of "text": a string, or a list of strings and
// {"type":…, "text":…} entities (links, bold, mentions).
func flattenText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		var ps string
		if json.Unmarshal(p, &ps) == nil {
			sb.WriteString(ps)
			continue
		}
		var ent struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(p, &ent) == nil {
			sb.WriteString(ent.Text)
		}
	}
	return sb.String()
}

func unix(s string) time.Time {
	n, _ := strconv.ParseInt(s, 10, 64)
	return time.Unix(n, 0)
}
