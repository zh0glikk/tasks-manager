package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrAlreadyReverted = errors.New("change already reverted")
	ErrNotRevertible   = errors.New("change can't be reverted")
)

// JSON tags match column names so a to_jsonb(row) snapshot from the change
// log decodes straight back into the struct when a change is reverted.

type Course struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Short   string `json:"short"`
	Teacher string `json:"teacher"`
	Link    string `json:"link"`
	Color   string `json:"color"`
}

type Event struct {
	ID        int64  `json:"id"`
	SeriesID  *int64 `json:"series_id"`
	CourseID  *int64 `json:"course_id"`
	Kind      string `json:"kind"`
	Date      string `json:"date"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Teacher   string `json:"teacher"`
	Link      string `json:"link"`
	Note      string `json:"note"`
	Tentative bool   `json:"tentative"`
}

type Assignment struct {
	ID          int64     `json:"id"`
	CourseID    *int64    `json:"course_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	DueDate     string    `json:"due_date"`
	DueTime     string    `json:"due_time"`
	Link        string    `json:"link"`
	Author      string    `json:"author"`
	CreatedAt   time.Time `json:"created_at"`
}

type Post struct {
	ID        int64     `json:"id"`
	CourseID  *int64    `json:"course_id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Pinned    bool      `json:"pinned"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

type Change struct {
	ID         int64           `json:"id"`
	Entity     string          `json:"entity"`
	EntityID   int64           `json:"entity_id"`
	Action     string          `json:"action"`
	Author     string          `json:"author"`
	Summary    string          `json:"summary"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	CreatedAt  time.Time       `json:"created_at"`
	RevertedAt *time.Time      `json:"reverted_at"`
}

const (
	EntityCourse     = "course"
	EntityEvent      = "event"
	EntitySeries     = "event_series"
	EntityAssignment = "assignment"
	EntityPost       = "post"
	EntityLecture    = "lecture"

	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
	ActionRevert = "revert"
)

var tables = map[string]string{
	EntityCourse:     "courses",
	EntityEvent:      "events",
	EntityAssignment: "assignments",
	EntityPost:       "posts",
	EntityLecture:    "lectures",
}

type Store struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

// ---- courses ----

const courseCols = `id, name, short, teacher, link, color`

func scanCourse(row pgx.Row) (Course, error) {
	var c Course
	err := row.Scan(&c.ID, &c.Name, &c.Short, &c.Teacher, &c.Link, &c.Color)
	return c, err
}

func (s *Store) ListCourses(ctx context.Context) ([]Course, error) {
	rows, err := s.db.Query(ctx, `select `+courseCols+` from courses where deleted_at is null order by id`)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanCourse)
}

func (s *Store) CreateCourse(ctx context.Context, author string, c Course) (int64, error) {
	return s.write(ctx, author, EntityCourse, ActionCreate, 0, func(tx pgx.Tx) (int64, error) {
		var id int64
		err := tx.QueryRow(ctx, `insert into courses (name, short, teacher, link, color)
			values ($1, $2, $3, $4, $5) returning id`,
			c.Name, c.Short, c.Teacher, c.Link, c.Color).Scan(&id)
		return id, err
	})
}

func updateCourse(ctx context.Context, tx pgx.Tx, c Course) error {
	return expectOne(tx.Exec(ctx, `update courses set name = $2, short = $3, teacher = $4, link = $5, color = $6
		where id = $1 and deleted_at is null`,
		c.ID, c.Name, c.Short, c.Teacher, c.Link, c.Color))
}

func (s *Store) UpdateCourse(ctx context.Context, author string, c Course) error {
	_, err := s.write(ctx, author, EntityCourse, ActionUpdate, c.ID, func(tx pgx.Tx) (int64, error) {
		return c.ID, updateCourse(ctx, tx, c)
	})
	return err
}

// ---- events ----

const eventCols = `id, series_id, course_id, kind, to_char(date, 'YYYY-MM-DD'), to_char(start_time, 'HH24:MI'),
	coalesce(to_char(end_time, 'HH24:MI'), ''), teacher, link, note, tentative`

func scanEvent(row pgx.Row) (Event, error) {
	var e Event
	err := row.Scan(&e.ID, &e.SeriesID, &e.CourseID, &e.Kind, &e.Date, &e.StartTime, &e.EndTime,
		&e.Teacher, &e.Link, &e.Note, &e.Tentative)
	return e, err
}

func (s *Store) ListEvents(ctx context.Context, from, to string) ([]Event, error) {
	rows, err := s.db.Query(ctx, `select `+eventCols+` from events
		where deleted_at is null and date between $1::date and $2::date
		order by date, start_time`, from, to)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanEvent)
}

func insertEvent(ctx context.Context, tx pgx.Tx, e Event) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `insert into events
		(series_id, course_id, kind, date, start_time, end_time, teacher, link, note, tentative)
		values ($1, $2, $3, $4::date, $5::time, nullif($6, '')::time, $7, $8, $9, $10) returning id`,
		e.SeriesID, e.CourseID, e.Kind, e.Date, e.StartTime, e.EndTime, e.Teacher, e.Link, e.Note, e.Tentative).Scan(&id)
	return id, err
}

func updateEvent(ctx context.Context, tx pgx.Tx, e Event) error {
	return expectOne(tx.Exec(ctx, `update events set course_id = $2, kind = $3, date = $4::date, start_time = $5::time,
		end_time = nullif($6, '')::time, teacher = $7, link = $8, note = $9, tentative = $10
		where id = $1 and deleted_at is null`,
		e.ID, e.CourseID, e.Kind, e.Date, e.StartTime, e.EndTime, e.Teacher, e.Link, e.Note, e.Tentative))
}

// CreateEvents inserts one event per date. More than one date makes a series,
// logged as a single change so it can be reverted in one step.
func (s *Store) CreateEvents(ctx context.Context, author string, e Event, dates []string) ([]int64, error) {
	if len(dates) == 1 {
		e.Date = dates[0]
		id, err := s.write(ctx, author, EntityEvent, ActionCreate, 0, func(tx pgx.Tx) (int64, error) {
			return insertEvent(ctx, tx, e)
		})
		return []int64{id}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	ids := make([]int64, 0, len(dates))
	for i, d := range dates {
		e.Date = d
		id, err := insertEvent(ctx, tx, e)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			e.SeriesID = &id
			if _, err := tx.Exec(ctx, `update events set series_id = $1 where id = $1`, id); err != nil {
				return nil, err
			}
		}
		ids = append(ids, id)
	}

	after, err := seriesSnapshot(ctx, tx, *e.SeriesID)
	if err != nil {
		return nil, err
	}
	if err := logChange(ctx, tx, EntitySeries, *e.SeriesID, ActionCreate, author, nil, after); err != nil {
		return nil, err
	}
	return ids, tx.Commit(ctx)
}

func (s *Store) UpdateEvent(ctx context.Context, author string, e Event) error {
	_, err := s.write(ctx, author, EntityEvent, ActionUpdate, e.ID, func(tx pgx.Tx) (int64, error) {
		return e.ID, updateEvent(ctx, tx, e)
	})
	return err
}

func seriesSnapshot(ctx context.Context, tx pgx.Tx, seriesID int64) ([]byte, error) {
	var b []byte
	err := tx.QueryRow(ctx, `select jsonb_agg(to_jsonb(e) order by e.date) from events e
		where e.series_id = $1 and e.deleted_at is null`, seriesID).Scan(&b)
	return b, err
}

// ---- assignments ----

const assignmentCols = `id, course_id, title, description, to_char(due_date, 'YYYY-MM-DD'),
	coalesce(to_char(due_time, 'HH24:MI'), ''), link, author, created_at`

func scanAssignment(row pgx.Row) (Assignment, error) {
	var a Assignment
	err := row.Scan(&a.ID, &a.CourseID, &a.Title, &a.Description, &a.DueDate, &a.DueTime, &a.Link, &a.Author, &a.CreatedAt)
	return a, err
}

func (s *Store) ListAssignments(ctx context.Context, from, to string) ([]Assignment, error) {
	rows, err := s.db.Query(ctx, `select `+assignmentCols+` from assignments
		where deleted_at is null and due_date between $1::date and $2::date
		order by due_date, due_time nulls last, id`, from, to)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanAssignment)
}

func (s *Store) CreateAssignment(ctx context.Context, author string, a Assignment) (int64, error) {
	return s.write(ctx, author, EntityAssignment, ActionCreate, 0, func(tx pgx.Tx) (int64, error) {
		var id int64
		err := tx.QueryRow(ctx, `insert into assignments (course_id, title, description, due_date, due_time, link, author)
			values ($1, $2, $3, $4::date, nullif($5, '')::time, $6, $7) returning id`,
			a.CourseID, a.Title, a.Description, a.DueDate, a.DueTime, a.Link, author).Scan(&id)
		return id, err
	})
}

func updateAssignment(ctx context.Context, tx pgx.Tx, a Assignment) error {
	return expectOne(tx.Exec(ctx, `update assignments set course_id = $2, title = $3, description = $4,
		due_date = $5::date, due_time = nullif($6, '')::time, link = $7
		where id = $1 and deleted_at is null`,
		a.ID, a.CourseID, a.Title, a.Description, a.DueDate, a.DueTime, a.Link))
}

func (s *Store) UpdateAssignment(ctx context.Context, author string, a Assignment) error {
	_, err := s.write(ctx, author, EntityAssignment, ActionUpdate, a.ID, func(tx pgx.Tx) (int64, error) {
		return a.ID, updateAssignment(ctx, tx, a)
	})
	return err
}

// ---- posts ----

const postCols = `id, course_id, title, body, pinned, author, created_at`

func scanPost(row pgx.Row) (Post, error) {
	var p Post
	err := row.Scan(&p.ID, &p.CourseID, &p.Title, &p.Body, &p.Pinned, &p.Author, &p.CreatedAt)
	return p, err
}

func (s *Store) ListPosts(ctx context.Context, limit int) ([]Post, error) {
	rows, err := s.db.Query(ctx, `select `+postCols+` from posts where deleted_at is null
		order by pinned desc, created_at desc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanPost)
}

func (s *Store) CreatePost(ctx context.Context, author string, p Post) (int64, error) {
	return s.write(ctx, author, EntityPost, ActionCreate, 0, func(tx pgx.Tx) (int64, error) {
		var id int64
		err := tx.QueryRow(ctx, `insert into posts (course_id, title, body, pinned, author)
			values ($1, $2, $3, $4, $5) returning id`,
			p.CourseID, p.Title, p.Body, p.Pinned, author).Scan(&id)
		return id, err
	})
}

func updatePost(ctx context.Context, tx pgx.Tx, p Post) error {
	return expectOne(tx.Exec(ctx, `update posts set course_id = $2, title = $3, body = $4, pinned = $5
		where id = $1 and deleted_at is null`,
		p.ID, p.CourseID, p.Title, p.Body, p.Pinned))
}

func (s *Store) UpdatePost(ctx context.Context, author string, p Post) error {
	_, err := s.write(ctx, author, EntityPost, ActionUpdate, p.ID, func(tx pgx.Tx) (int64, error) {
		return p.ID, updatePost(ctx, tx, p)
	})
	return err
}

// ---- delete (soft) ----

func (s *Store) Delete(ctx context.Context, author, entity string, id int64) error {
	table, ok := tables[entity]
	if !ok {
		return ErrNotFound
	}
	_, err := s.write(ctx, author, entity, ActionDelete, id, func(tx pgx.Tx) (int64, error) {
		return id, expectOne(tx.Exec(ctx, `update `+table+` set deleted_at = now() where id = $1 and deleted_at is null`, id))
	})
	return err
}

// ---- change log ----

func (s *Store) ListChanges(ctx context.Context, limit int) ([]Change, error) {
	rows, err := s.db.Query(ctx, `select id, entity, entity_id, action, author, summary, before, after, created_at, reverted_at
		from changes order by id desc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row pgx.Row) (Change, error) {
		var c Change
		err := row.Scan(&c.ID, &c.Entity, &c.EntityID, &c.Action, &c.Author, &c.Summary, &c.Before, &c.After, &c.CreatedAt, &c.RevertedAt)
		return c, err
	})
}

// Revert undoes a single logged change: a create is soft-deleted, a delete is
// restored, an update is rolled back to its "before" snapshot.
func (s *Store) Revert(ctx context.Context, author string, changeID int64) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var c Change
	err = tx.QueryRow(ctx, `select entity, entity_id, action, before, reverted_at from changes where id = $1 for update`, changeID).
		Scan(&c.Entity, &c.EntityID, &c.Action, &c.Before, &c.RevertedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if c.RevertedAt != nil {
		return ErrAlreadyReverted
	}

	entity, table := c.Entity, tables[c.Entity]
	if c.Entity == EntitySeries {
		table = "events"
	}
	if table == "" {
		return ErrNotRevertible
	}

	var before []byte
	if c.Entity == EntitySeries {
		before, err = seriesSnapshot(ctx, tx, c.EntityID)
	} else {
		before, err = snapshot(ctx, tx, table, c.EntityID, true)
	}
	if err != nil {
		return err
	}

	switch {
	case c.Action == ActionCreate && c.Entity == EntitySeries:
		_, err = tx.Exec(ctx, `update events set deleted_at = now() where series_id = $1 and deleted_at is null`, c.EntityID)
	case c.Action == ActionCreate:
		_, err = tx.Exec(ctx, `update `+table+` set deleted_at = now() where id = $1`, c.EntityID)
	case c.Action == ActionDelete:
		_, err = tx.Exec(ctx, `update `+table+` set deleted_at = null where id = $1`, c.EntityID)
	case c.Action == ActionUpdate:
		err = restore(ctx, tx, entity, c.Before)
	default:
		return ErrNotRevertible
	}
	if err != nil {
		return err
	}

	var after []byte
	if c.Entity == EntitySeries {
		after, err = seriesSnapshot(ctx, tx, c.EntityID)
	} else {
		after, err = snapshot(ctx, tx, table, c.EntityID, true)
	}
	if err != nil {
		return err
	}
	if err := logChange(ctx, tx, entity, c.EntityID, ActionRevert, author, before, after); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update changes set reverted_at = now() where id = $1`, changeID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func restore(ctx context.Context, tx pgx.Tx, entity string, before []byte) error {
	switch entity {
	case EntityCourse:
		var v Course
		if err := json.Unmarshal(before, &v); err != nil {
			return err
		}
		return updateCourse(ctx, tx, v)
	case EntityEvent:
		var v Event
		if err := json.Unmarshal(before, &v); err != nil {
			return err
		}
		return updateEvent(ctx, tx, v)
	case EntityAssignment:
		var v Assignment
		if err := json.Unmarshal(before, &v); err != nil {
			return err
		}
		return updateAssignment(ctx, tx, v)
	case EntityPost:
		var v Post
		if err := json.Unmarshal(before, &v); err != nil {
			return err
		}
		return updatePost(ctx, tx, v)
	case EntityLecture:
		var v Lecture
		if err := json.Unmarshal(before, &v); err != nil {
			return err
		}
		return updateLecture(ctx, tx, v)
	}
	return ErrNotRevertible
}

// ---- helpers ----

// write runs apply in a transaction and logs the change with before/after snapshots.
func (s *Store) write(ctx context.Context, author, entity, action string, id int64, apply func(pgx.Tx) (int64, error)) (int64, error) {
	table := tables[entity]
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var before []byte
	if id != 0 {
		if before, err = snapshot(ctx, tx, table, id, false); err != nil {
			return 0, err
		}
	}
	id, err = apply(tx)
	if err != nil {
		return 0, err
	}
	after, err := snapshot(ctx, tx, table, id, true)
	if err != nil {
		return 0, err
	}
	if action == ActionDelete {
		after = nil
	}
	if err := logChange(ctx, tx, entity, id, action, author, before, after); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func snapshot(ctx context.Context, tx pgx.Tx, table string, id int64, withDeleted bool) ([]byte, error) {
	q := `select to_jsonb(t) from ` + table + ` t where id = $1`
	if !withDeleted {
		q += ` and deleted_at is null`
	}
	var b []byte
	err := tx.QueryRow(ctx, q, id).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

func logChange(ctx context.Context, tx pgx.Tx, entity string, id int64, action, author string, before, after []byte) error {
	_, err := tx.Exec(ctx, `insert into changes (entity, entity_id, action, author, summary, before, after)
		values ($1, $2, $3, $4, $5, $6, $7)`,
		entity, id, action, author, summarize(after, before), before, after)
	return err
}

// summarize picks a human label for the change log from a row snapshot.
func summarize(snaps ...[]byte) string {
	for _, b := range snaps {
		if len(b) == 0 {
			continue
		}
		var rows []map[string]any
		if b[0] == '[' {
			if json.Unmarshal(b, &rows) != nil || len(rows) == 0 {
				continue
			}
		} else {
			var m map[string]any
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			rows = []map[string]any{m}
		}
		m := rows[0]
		for _, k := range []string{"title", "name"} {
			if v, _ := m[k].(string); v != "" {
				return v
			}
		}
		kind, _ := m["kind"].(string)
		date, _ := m["date"].(string)
		if len(rows) > 1 {
			return kind + " · " + strconv.Itoa(len(rows)) + " дат з " + date
		}
		return kind + " · " + date
	}
	return ""
}

func expectOne(tag interface{ RowsAffected() int64 }, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func collect[T any](rows pgx.Rows, scan func(pgx.Row) (T, error)) ([]T, error) {
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
