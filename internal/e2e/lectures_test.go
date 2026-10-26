package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zh0glikk/tasks-manager/internal/api"
	"github.com/zh0glikk/tasks-manager/internal/keyauth"
	"github.com/zh0glikk/tasks-manager/internal/store"
)

func TestLectures(t *testing.T) {
	st, bot, _, mcpURL := setup(t)
	ctx := context.Background()
	site := httptest.NewServer(api.New(st, fstest.MapFS{}, api.Options{Auth: keyauth.Verifier{Store: st, Members: bot}}))
	defer site.Close()

	do := func(method, path, key, body string) (int, json.RawMessage) {
		t.Helper()
		req, _ := http.NewRequest(method, site.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out json.RawMessage
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	reader, _, err := st.CreateKey(ctx, "Читач", nil, store.ScopeRead, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	writer, _, err := st.CreateKey(ctx, "Староста", nil, store.ScopeWrite, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	lecture := `{"course_id": 2, "date": "2026-10-23", "title": "Засідання аспірантів",
		"video_url": "https://www.youtube.com/watch?v=cX087zK25n8", "duration_s": 3989,
		"transcript": [
			{"start": 0, "end": 13, "text": "Давайте розпочинати."},
			{"start": 480, "end": 492, "text": "Тема дисертації затверджується радою до 20 листопада."},
			{"start": 3360, "end": 3370, "text": "Декларація академічної доброчесності."}
		]}`

	// reading is public, writing needs write access
	if code, _ := do("GET", "/api/lectures", "", ""); code != 200 {
		t.Fatalf("anonymous list: %d", code)
	}
	if code, _ := do("POST", "/api/lectures", "", lecture); code != 401 {
		t.Fatalf("anonymous upload: %d", code)
	}
	if code, _ := do("POST", "/api/lectures", reader, lecture); code != 403 {
		t.Fatalf("read-only upload: %d", code)
	}
	if code, out := do("POST", "/api/lectures", writer, strings.Replace(lecture, `"course_id": 2`, `"course_id": 999`, 1)); code != 400 {
		t.Fatalf("unknown course: %d %s", code, out)
	}
	code, out := do("POST", "/api/lectures", writer, lecture)
	if code != 200 {
		t.Fatalf("upload: %d %s", code, out)
	}
	var created struct{ ID int64 }
	json.Unmarshal(out, &created)

	code, out = do("GET", "/api/lectures", reader, "")
	var list []store.Lecture
	json.Unmarshal(out, &list)
	if code != 200 || len(list) != 1 || list[0].Segments != 3 || list[0].Author != "Староста" {
		t.Fatalf("list: %d %s", code, out)
	}
	code, out = do("GET", fmt.Sprintf("/api/lectures/%d", created.ID), reader, "")
	if code != 200 || !strings.Contains(string(out), "Декларація") {
		t.Fatalf("get: %d %s", code, out)
	}

	// word prefixes find inflected forms
	code, out = do("GET", "/api/lectures/search?q="+strings.ReplaceAll("дисертація листопад", " ", "+"), reader, "")
	var hits []store.SegmentHit
	json.Unmarshal(out, &hits)
	if code != 200 || len(hits) != 1 || hits[0].Start != 480 {
		t.Fatalf("search: %d %s", code, out)
	}
	// a misrecognized word doesn't hide the passage: any word matches when all don't
	_, out = do("GET", "/api/lectures/search?q=декларація+доброчисності", reader, "")
	json.Unmarshal(out, &hits)
	if len(hits) != 1 || hits[0].Start != 3360 {
		t.Fatalf("fallback search: %s", out)
	}

	// editing the summary keeps the transcript
	edit := `{"course_id": 2, "date": "2026-10-23", "title": "Засідання аспірантів", "summary": "Тема до 20.11 [08:00]", "duration_s": 3989,
		"video_url": "https://www.youtube.com/watch?v=cX087zK25n8"}`
	if code, out := do("PUT", fmt.Sprintf("/api/lectures/%d", created.ID), writer, edit); code != 200 {
		t.Fatalf("edit: %d %s", code, out)
	}
	if code, out := do("PUT", fmt.Sprintf("/api/lectures/%d", created.ID), writer, strings.Replace(edit, `"date"`, `"event_id": 99999, "date"`, 1)); code != 400 {
		t.Fatalf("unknown event: %d %s", code, out)
	}
	l, _ := st.GetLecture(ctx, created.ID)
	if l.Summary != "Тема до 20.11 [08:00]" || l.Segments != 3 {
		t.Fatalf("after edit: %+v", l)
	}

	// MCP: read tools for any key, update_lecture needs write access
	rs, err := connect(t, mcpURL, reader)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	if out, _ := call(t, rs, "lectures", nil); !strings.Contains(out, fmt.Sprintf("лекція #%d 2026-10-23 (пт) Засідання аспірантів · Англійська · 1:06:29", created.ID)) {
		t.Fatalf("lectures: %q", out)
	}
	out2, _ := call(t, rs, "lecture_transcript", map[string]any{"id": created.ID, "from": "08:00"})
	if !strings.Contains(out2, "[08:00] Тема дисертації") || strings.Contains(out2, "Давайте розпочинати") || strings.Contains(out2, "Конспект:") {
		t.Fatalf("lecture_transcript from 08:00: %q", out2)
	}
	if out, _ := call(t, rs, "search_lectures", map[string]any{"query": "доброчесн"}); !strings.Contains(out, "[56:00]") || !strings.Contains(out, "t=3360") {
		t.Fatalf("search_lectures: %q", out)
	}
	if out, isErr := call(t, rs, "update_lecture", map[string]any{"id": created.ID, "summary": "x"}); !isErr || !strings.Contains(out, "/grant") {
		t.Fatalf("read-only update_lecture: %q", out)
	}
	ws, err := connect(t, mcpURL, writer)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if out, isErr := call(t, ws, "update_lecture", map[string]any{"id": created.ID, "summary": "Новий конспект"}); isErr {
		t.Fatalf("update_lecture: %q", out)
	}

	// reverting restores the previous summary
	changes, _ := st.ListChanges(ctx, 1)
	if changes[0].Author != "Староста (через Claude)" {
		t.Fatalf("author: %q", changes[0].Author)
	}
	if err := st.Revert(ctx, "Адмін", changes[0].ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := st.GetLecture(ctx, created.ID); l.Summary != "Тема до 20.11 [08:00]" {
		t.Fatalf("after revert: %q", l.Summary)
	}

	// deleting hides it everywhere
	if code, _ := do("DELETE", fmt.Sprintf("/api/lectures/%d", created.ID), writer, `{}`); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if _, out := do("GET", "/api/lectures/search?q=дисертація", reader, ""); string(out) != "[]" {
		t.Fatalf("deleted lecture still found: %s", out)
	}
}
