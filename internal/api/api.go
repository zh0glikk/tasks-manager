package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zh0glikk/tasks-manager/internal/keyauth"
	"github.com/zh0glikk/tasks-manager/internal/ratelimit"
	"github.com/zh0glikk/tasks-manager/internal/store"
	"github.com/zh0glikk/tasks-manager/internal/validate"
)

const (
	maxSeries      = 60
	writesPerIP    = 30
	writesInterval = time.Minute
	maxBody        = 64 << 10
	maxLectureBody = 8 << 20 // a transcript of a long class
)

type API struct {
	store *store.Store
	auth  keyauth.Verifier
	lim   *ratelimit.Limiter
	open  bool
}

type Options struct {
	Auth keyauth.Verifier
	// Open lets anyone edit without a key (local development only).
	Open bool
	// BotUsername returns the Telegram bot's username for the "get a key" link; may be nil.
	BotUsername func() string
}

// New builds the HTTP handler. Reads are public except lectures, which need any
// bot-issued key; writes need a key with write access unless opts.Open is set.
func New(s *store.Store, static fs.FS, opts Options) http.Handler {
	lim := ratelimit.New(writesPerIP, writesInterval)
	a := &API{store: s, auth: opts.Auth, lim: lim, open: opts.Open}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		bot := ""
		if opts.BotUsername != nil {
			bot = opts.BotUsername()
		}
		writeJSON(w, http.StatusOK, map[string]any{"auth_required": !opts.Open, "bot_username": bot})
	})
	mux.HandleFunc("GET /api/me", a.me)

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

	mux.HandleFunc("GET /api/lectures", a.listLectures)
	mux.HandleFunc("GET /api/lectures/search", a.searchLectures)
	mux.HandleFunc("GET /api/lectures/{id}", a.getLecture)
	mux.HandleFunc("POST /api/lectures", a.createLecture)
	mux.HandleFunc("PUT /api/lectures/{id}", a.updateLecture)
	mux.HandleFunc("DELETE /api/lectures/{id}", a.delete(store.EntityLecture))

	mux.HandleFunc("GET /api/changes", a.listChanges)
	mux.HandleFunc("POST /api/changes/{id}/revert", a.revert)

	mux.Handle("GET /", http.FileServerFS(static))

	return logRequests(guard(opts.Auth, opts.Open, lim, mux))
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
	id, err := a.store.CreateCourse(r.Context(), a.author(r, req.Author), req.Course)
	respond(w, map[string]int64{"id": id}, err)
}

func (a *API) updateCourse(w http.ResponseWriter, r *http.Request) {
	var req courseReq
	if !decode(w, r, &req) || !pathID(w, r, &req.ID) || !validCourse(w, &req.Course) {
		return
	}
	respond(w, nil, a.store.UpdateCourse(r.Context(), a.author(r, req.Author), req.Course))
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
	ids, err := a.store.CreateEvents(r.Context(), a.author(r, req.Author), req.Event, dates)
	respond(w, map[string][]int64{"ids": ids}, err)
}

func (a *API) updateEvent(w http.ResponseWriter, r *http.Request) {
	var req eventReq
	if !decode(w, r, &req) || !pathID(w, r, &req.ID) || !validEvent(w, &req.Event) {
		return
	}
	respond(w, nil, a.store.UpdateEvent(r.Context(), a.author(r, req.Author), req.Event))
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
	id, err := a.store.CreateAssignment(r.Context(), a.author(r, req.Author), req.Assignment)
	respond(w, map[string]int64{"id": id}, err)
}

func (a *API) updateAssignment(w http.ResponseWriter, r *http.Request) {
	var req assignmentReq
	if !decode(w, r, &req) || !pathID(w, r, &req.ID) || !validAssignment(w, &req.Assignment) {
		return
	}
	respond(w, nil, a.store.UpdateAssignment(r.Context(), a.author(r, req.Author), req.Assignment))
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
	id, err := a.store.CreatePost(r.Context(), a.author(r, req.Author), req.Post)
	respond(w, map[string]int64{"id": id}, err)
}

func (a *API) updatePost(w http.ResponseWriter, r *http.Request) {
	var req postReq
	if !decode(w, r, &req) || !pathID(w, r, &req.ID) || !validPost(w, &req.Post) {
		return
	}
	respond(w, nil, a.store.UpdatePost(r.Context(), a.author(r, req.Author), req.Post))
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
		respond(w, nil, a.store.Delete(r.Context(), a.author(r, req.Author), entity, id))
	}
}

// ---- lectures (members only) ----

// member lets the request through with any valid key, read-only included.
func (a *API) member(w http.ResponseWriter, r *http.Request) bool {
	if a.open {
		return true
	}
	if _, status, msg := authenticate(r, a.auth); status != 0 {
		writeJSON(w, status, map[string]any{"error": msg, "login_required": status == http.StatusUnauthorized})
		return false
	}
	return true
}

func (a *API) listLectures(w http.ResponseWriter, r *http.Request) {
	if !a.member(w, r) {
		return
	}
	v, err := a.store.ListLectures(r.Context(), int64Param(r, "course_id"))
	respond(w, v, err)
}

func (a *API) searchLectures(w http.ResponseWriter, r *http.Request) {
	if !a.member(w, r) {
		return
	}
	v, err := a.store.SearchSegments(r.Context(), r.URL.Query().Get("q"), int64Param(r, "course_id"), intParam(r, "limit", 50, 200))
	respond(w, v, err)
}

func (a *API) getLecture(w http.ResponseWriter, r *http.Request) {
	var id int64
	if !a.member(w, r) || !pathID(w, r, &id) {
		return
	}
	l, err := a.store.GetLecture(r.Context(), id)
	if err != nil {
		respond(w, nil, err)
		return
	}
	segs, err := a.store.Segments(r.Context(), id, 0, 0)
	respond(w, map[string]any{"lecture": l, "transcript": segs}, err)
}

type lectureReq struct {
	store.Lecture
	Author string `json:"author"`
	// Transcript replaces the stored one; omitted on update keeps it.
	Transcript []store.Segment `json:"transcript"`
}

func (a *API) createLecture(w http.ResponseWriter, r *http.Request) {
	var req lectureReq
	if !decodeLimit(w, r, &req, maxLectureBody) || !validLecture(w, &req) {
		return
	}
	id, err := a.store.CreateLecture(r.Context(), a.author(r, req.Author), req.Lecture, req.Transcript)
	respond(w, map[string]int64{"id": id}, err)
}

func (a *API) updateLecture(w http.ResponseWriter, r *http.Request) {
	var req lectureReq
	if !decodeLimit(w, r, &req, maxLectureBody) || !pathID(w, r, &req.ID) || !validLecture(w, &req) {
		return
	}
	respond(w, nil, a.store.UpdateLecture(r.Context(), a.author(r, req.Author), req.Lecture, req.Transcript))
}

func (a *API) listChanges(w http.ResponseWriter, r *http.Request) {
	v, err := a.store.ListChanges(r.Context(), intParam(r, "limit", 50, 200))
	// the change log is public, lecture contents are not
	for i := range v {
		if v[i].Entity == store.EntityLecture {
			v[i].Before, v[i].After = nil, nil
		}
	}
	respond(w, v, err)
}

func (a *API) revert(w http.ResponseWriter, r *http.Request) {
	var req authorReq
	var id int64
	if !decode(w, r, &req) || !pathID(w, r, &id) {
		return
	}
	respond(w, nil, a.store.Revert(r.Context(), a.author(r, req.Author), id))
}

// ---- validation ----

func validCourse(w http.ResponseWriter, c *store.Course) bool { return valid(w, validate.Course(c)) }
func validEvent(w http.ResponseWriter, e *store.Event) bool   { return valid(w, validate.Event(e)) }
func validAssignment(w http.ResponseWriter, a *store.Assignment) bool {
	return valid(w, validate.Assignment(a))
}
func validPost(w http.ResponseWriter, p *store.Post) bool { return valid(w, validate.Post(p)) }
func validLecture(w http.ResponseWriter, l *lectureReq) bool {
	return valid(w, validate.Lecture(&l.Lecture)) && valid(w, validate.Segments(l.Transcript))
}

func valid(w http.ResponseWriter, err error) bool {
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// me tells the UI who is logged in and whether they may edit.
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	if ok, _ := a.lim.Allow("me:" + clientIP(r)); !ok {
		fail(w, http.StatusTooManyRequests, "забагато спроб, спробуй за хвилину")
		return
	}
	k, status, msg := authenticate(r, a.auth)
	if status != 0 {
		fail(w, status, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": k.Name, "can_write": k.CanWrite()})
}

// author is the key owner's name when logged in, else the name the client sent.
func (a *API) author(r *http.Request, fallback string) string {
	if k, ok := keyFrom(r.Context()); ok {
		return author(k.Name)
	}
	return author(fallback)
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
	if !validate.Date(from) || !validate.Date(to) {
		fail(w, http.StatusBadRequest, "from і to мають бути у форматі YYYY-MM-DD")
		return "", "", false
	}
	return from, to, true
}

// ---- plumbing ----

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeLimit(w, r, v, maxBody)
}

func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
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

func int64Param(r *http.Request, name string) *int64 {
	v, err := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	if err != nil || v <= 0 {
		return nil
	}
	return &v
}

func respond(w http.ResponseWriter, v any, err error) {
	switch {
	case errors.Is(err, store.ErrBadRef):
		fail(w, http.StatusBadRequest, "такого предмета або заняття немає")
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
