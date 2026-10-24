package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zh0glikk/tasks-manager/internal/store"
)

const (
	maxSeries      = 60
	writesPerIP    = 30
	writesInterval = time.Minute
)

var (
	reDate  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reTime  = regexp.MustCompile(`^\d{2}:\d{2}$`)
	reColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

type API struct {
	store *store.Store
}

// New builds the HTTP handler. A non-empty editCode makes every write require
// a matching X-Edit-Code header; reads stay public.
func New(s *store.Store, static fs.FS, editCode string) http.Handler {
	a := &API{store: s}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"edit_protected": editCode != ""})
	})

	mux.HandleFunc("GET /api/courses", a.listCourses)
	mux.HandleFunc("POST /api/courses", a.createCourse)
	mux.HandleFunc("PUT /api/courses/{id}", a.updateCourse)
	mux.HandleFunc("DELETE /api/courses/{id}", a.delete(store.EntityCourse))

	mux.HandleFunc("GET /api/events", a.listEvents)
	mux.HandleFunc("POST /api/events", a.createEvents)
	mux.HandleFunc("PUT /api/events/{id}", a.updateEvent)
	mux.HandleFunc("DELETE /api/events/{id}", a.delete(store.EntityEvent))

	mux.HandleFunc("GET /api/assignments", a.listAssignments)
	mux.HandleFunc("POST /api/assignments", a.createAssignment)
	mux.HandleFunc("PUT /api/assignments/{id}", a.updateAssignment)
	mux.HandleFunc("DELETE /api/assignments/{id}", a.delete(store.EntityAssignment))

	mux.HandleFunc("GET /api/posts", a.listPosts)
	mux.HandleFunc("POST /api/posts", a.createPost)
	mux.HandleFunc("PUT /api/posts/{id}", a.updatePost)
	mux.HandleFunc("DELETE /api/posts/{id}", a.delete(store.EntityPost))

	mux.HandleFunc("GET /api/changes", a.listChanges)
	mux.HandleFunc("POST /api/changes/{id}/revert", a.revert)

	mux.Handle("GET /", http.FileServerFS(static))

	return logRequests(guard(editCode, newLimiter(writesPerIP, writesInterval), mux))
}

// ---- handlers ----

func (a *API) listCourses(w http.ResponseWriter, r *http.Request) {
	v, err := a.store.ListCourses(r.Context())
	respond(w, v, err)
}

type courseReq struct {
	store.Course
	Author string `json:"author"`
}

func (a *API) createCourse(w http.ResponseWriter, r *http.Request) {
	var req courseReq
	if !decode(w, r, &req) || !validCourse(w, &req.Course) {
		return
	}
	id, err := a.store.CreateCourse(r.Context(), author(req.Author), req.Course)
	respond(w, map[string]int64{"id": id}, err)
}

func (a *API) updateCourse(w http.ResponseWriter, r *http.Request) {
	var req courseReq
	if !decode(w, r, &req) || !pathID(w, r, &req.ID) || !validCourse(w, &req.Course) {
		return
	}
	respond(w, nil, a.store.UpdateCourse(r.Context(), author(req.Author), req.Course))
}

func (a *API) listEvents(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(w, r)
	if !ok {
		return
	}
	v, err := a.store.ListEvents(r.Context(), from, to)
	respond(w, v, err)
}

type eventReq struct {
	store.Event
	Author      string `json:"author"`
	RepeatUntil string `json:"repeat_until"`
}

func (a *API) createEvents(w http.ResponseWriter, r *http.Request) {
	var req eventReq
	if !decode(w, r, &req) || !validEvent(w, &req.Event) {
		return
	}
	dates := []string{req.Date}
	if req.RepeatUntil != "" {
		var ok bool
		if dates, ok = weekly(req.Date, req.RepeatUntil); !ok {
			fail(w, http.StatusBadRequest, "повтор: дата «до» має бути після першої дати і не далі ніж на 60 тижнів")
			return
		}
	}
	req.SeriesID = nil
	ids, err := a.store.CreateEvents(r.Context(), author(req.Author), req.Event, dates)
	respond(w, map[string][]int64{"ids": ids}, err)
}

func (a *API) updateEvent(w http.ResponseWriter, r *http.Request) {
	var req eventReq
	if !decode(w, r, &req) || !pathID(w, r, &req.ID) || !validEvent(w, &req.Event) {
		return
	}
	respond(w, nil, a.store.UpdateEvent(r.Context(), author(req.Author), req.Event))
}

func (a *API) listAssignments(w http.ResponseWriter, r *http.Request) {
	from, to, ok := dateRange(w, r)
	if !ok {
		return
	}
	v, err := a.store.ListAssignments(r.Context(), from, to)
	respond(w, v, err)
}

type assignmentReq struct {
	store.Assignment
	Author string `json:"author"`
}

func (a *API) createAssignment(w http.ResponseWriter, r *http.Request) {
	var req assignmentReq
	if !decode(w, r, &req) || !validAssignment(w, &req.Assignment) {
		return
	}
	id, err := a.store.CreateAssignment(r.Context(), author(req.Author), req.Assignment)
	respond(w, map[string]int64{"id": id}, err)
}

func (a *API) updateAssignment(w http.ResponseWriter, r *http.Request) {
	var req assignmentReq
	if !decode(w, r, &req) || !pathID(w, r, &req.ID) || !validAssignment(w, &req.Assignment) {
		return
	}
	respond(w, nil, a.store.UpdateAssignment(r.Context(), author(req.Author), req.Assignment))
}

func (a *API) listPosts(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", 30, 100)
	v, err := a.store.ListPosts(r.Context(), limit)
	respond(w, v, err)
}

type postReq struct {
	store.Post
	Author string `json:"author"`
}

func (a *API) createPost(w http.ResponseWriter, r *http.Request) {
	var req postReq
	if !decode(w, r, &req) || !validPost(w, &req.Post) {
		return
	}
	id, err := a.store.CreatePost(r.Context(), author(req.Author), req.Post)
	respond(w, map[string]int64{"id": id}, err)
}

func (a *API) updatePost(w http.ResponseWriter, r *http.Request) {
	var req postReq
	if !decode(w, r, &req) || !pathID(w, r, &req.ID) || !validPost(w, &req.Post) {
		return
	}
	respond(w, nil, a.store.UpdatePost(r.Context(), author(req.Author), req.Post))
}

type authorReq struct {
	Author string `json:"author"`
}

func (a *API) delete(entity string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req authorReq
		var id int64
		if !decode(w, r, &req) || !pathID(w, r, &id) {
			return
		}
		respond(w, nil, a.store.Delete(r.Context(), author(req.Author), entity, id))
	}
}

func (a *API) listChanges(w http.ResponseWriter, r *http.Request) {
	v, err := a.store.ListChanges(r.Context(), intParam(r, "limit", 50, 200))
	respond(w, v, err)
}

func (a *API) revert(w http.ResponseWriter, r *http.Request) {
	var req authorReq
	var id int64
	if !decode(w, r, &req) || !pathID(w, r, &id) {
		return
	}
	respond(w, nil, a.store.Revert(r.Context(), author(req.Author), id))
}

// ---- validation ----

func validCourse(w http.ResponseWriter, c *store.Course) bool {
	trim(&c.Name, &c.Short, &c.Teacher, &c.Link, &c.Color)
	if c.Color == "" {
		c.Color = "#0E5D69"
	}
	return check(w,
		rule(c.Name != "", "вкажіть назву предмета"),
		rule(c.Short != "", "вкажіть коротку назву"),
		maxLen(c.Name, 200, "назва"), maxLen(c.Short, 30, "коротка назва"), maxLen(c.Teacher, 200, "викладач"),
		validLink(c.Link),
		rule(reColor.MatchString(c.Color), "колір у форматі #RRGGBB"),
	)
}

func validEvent(w http.ResponseWriter, e *store.Event) bool {
	trim(&e.Kind, &e.Date, &e.StartTime, &e.EndTime, &e.Teacher, &e.Link, &e.Note)
	return check(w,
		rule(validDate(e.Date), "вкажіть дату"),
		rule(reTime.MatchString(e.StartTime), "вкажіть час початку"),
		rule(e.EndTime == "" || reTime.MatchString(e.EndTime), "неправильний час закінчення"),
		rule(e.EndTime == "" || e.EndTime > e.StartTime, "кінець має бути пізніше початку"),
		maxLen(e.Kind, 40, "тип"), maxLen(e.Teacher, 200, "викладач"), maxLen(e.Note, 1000, "примітка"),
		validLink(e.Link),
	)
}

func validAssignment(w http.ResponseWriter, a *store.Assignment) bool {
	trim(&a.Title, &a.Description, &a.DueDate, &a.DueTime, &a.Link)
	return check(w,
		rule(a.Title != "", "вкажіть, що треба зробити"),
		maxLen(a.Title, 200, "назва"), maxLen(a.Description, 5000, "деталі"),
		rule(validDate(a.DueDate), "вкажіть дату дедлайну"),
		rule(a.DueTime == "" || reTime.MatchString(a.DueTime), "неправильний час"),
		validLink(a.Link),
	)
}

func validPost(w http.ResponseWriter, p *store.Post) bool {
	trim(&p.Title, &p.Body)
	return check(w,
		rule(p.Title != "", "вкажіть заголовок"),
		maxLen(p.Title, 200, "заголовок"), maxLen(p.Body, 5000, "текст"),
	)
}

func rule(ok bool, msg string) string {
	if ok {
		return ""
	}
	return msg
}

func maxLen(s string, n int, field string) string {
	return rule(utf8.RuneCountInString(s) <= n, field+": не більше "+strconv.Itoa(n)+" символів")
}

func validLink(s string) string {
	return rule(s == "" || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://"), "посилання має починатися з https://")
}

func validDate(s string) bool {
	if !reDate.MatchString(s) {
		return false
	}
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func check(w http.ResponseWriter, msgs ...string) bool {
	for _, m := range msgs {
		if m != "" {
			fail(w, http.StatusBadRequest, m)
			return false
		}
	}
	return true
}

func trim(ss ...*string) {
	for _, s := range ss {
		*s = strings.TrimSpace(*s)
	}
}

func author(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "анонім"
	}
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return s
}

// weekly returns dates from start to until (inclusive) stepping 7 days.
func weekly(start, until string) ([]string, bool) {
	s, err1 := time.Parse(time.DateOnly, start)
	u, err2 := time.Parse(time.DateOnly, until)
	if err1 != nil || err2 != nil || u.Before(s) {
		return nil, false
	}
	var out []string
	for d := s; !d.After(u); d = d.AddDate(0, 0, 7) {
		out = append(out, d.Format(time.DateOnly))
		if len(out) > maxSeries {
			return nil, false
		}
	}
	return out, true
}

func dateRange(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if !validDate(from) || !validDate(to) {
		fail(w, http.StatusBadRequest, "from і to мають бути у форматі YYYY-MM-DD")
		return "", "", false
	}
	return from, to, true
}

// ---- plumbing ----

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "неправильний JSON")
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request, id *int64) bool {
	v, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || v <= 0 {
		fail(w, http.StatusBadRequest, "неправильний id")
		return false
	}
	*id = v
	return true
}

func intParam(r *http.Request, name string, def, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || v <= 0 {
		return def
	}
	return min(v, max)
}

func respond(w http.ResponseWriter, v any, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "не знайдено — можливо, вже видалено")
	case errors.Is(err, store.ErrAlreadyReverted):
		fail(w, http.StatusConflict, "цю зміну вже повернуто")
	case errors.Is(err, store.ErrNotRevertible):
		fail(w, http.StatusBadRequest, "цю зміну не можна повернути")
	case err != nil:
		log.Printf("error: %v", err)
		fail(w, http.StatusInternalServerError, "помилка сервера")
	default:
		if v == nil {
			v = map[string]bool{"ok": true}
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}
