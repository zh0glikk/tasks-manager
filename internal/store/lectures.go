package store

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrBadRef means a referenced course or event doesn't exist.
var ErrBadRef = errors.New("referenced row not found")

// Lecture is a recording of a class. JSON tags match the columns (see Course).
type Lecture struct {
	ID        int64     `json:"id"`
	EventID   *int64    `json:"event_id"`
	CourseID  *int64    `json:"course_id"`
	Date      string    `json:"date"`
	Title     string    `json:"title"`
	VideoURL  string    `json:"video_url"`
	DurationS int       `json:"duration_s"`
	Summary   string    `json:"summary"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	// Segments is the transcript length; not a column.
	Segments int `json:"segments"`
}

type Segment struct {
	Start int    `json:"start"` // seconds from the start of the video
	End   int    `json:"end"`
	Text  string `json:"text"`
}

// SegmentHit is a search result: a transcript piece and the lecture it is from.
type SegmentHit struct {
	Segment
	LectureID int64  `json:"lecture_id"`
	CourseID  *int64 `json:"course_id"`
	Date      string `json:"date"`
	Title     string `json:"title"`
	VideoURL  string `json:"video_url"`
}

const lectureCols = `l.id, l.event_id, l.course_id, to_char(l.date, 'YYYY-MM-DD'), l.title, l.video_url, l.duration_s,
	l.summary, l.author, l.created_at, (select count(*) from lecture_segments s where s.lecture_id = l.id)`

func scanLecture(row pgx.Row) (Lecture, error) {
	var l Lecture
	err := row.Scan(&l.ID, &l.EventID, &l.CourseID, &l.Date, &l.Title, &l.VideoURL, &l.DurationS,
		&l.Summary, &l.Author, &l.CreatedAt, &l.Segments)
	return l, err
}

// ListLectures returns lectures newest first, optionally for one course.
func (s *Store) ListLectures(ctx context.Context, courseID *int64) ([]Lecture, error) {
	rows, err := s.db.Query(ctx, `select `+lectureCols+` from lectures l
		where l.deleted_at is null and ($1::bigint is null or l.course_id = $1)
		order by l.date desc, l.id desc`, courseID)
	if err != nil {
		return nil, err
	}
	return collect(rows, scanLecture)
}

func (s *Store) GetLecture(ctx context.Context, id int64) (Lecture, error) {
	l, err := scanLecture(s.db.QueryRow(ctx, `select `+lectureCols+` from lectures l
		where l.id = $1 and l.deleted_at is null`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// CreateLecture stores a lecture with its transcript. Only the lecture row
// goes into the change log, not the transcript.
func (s *Store) CreateLecture(ctx context.Context, author string, l Lecture, segs []Segment) (int64, error) {
	return s.write(ctx, author, EntityLecture, ActionCreate, 0, func(tx pgx.Tx) (int64, error) {
		var id int64
		err := tx.QueryRow(ctx, `insert into lectures (event_id, course_id, date, title, video_url, duration_s, summary, author)
			values ($1, $2, $3::date, $4, $5, $6, $7, $8) returning id`,
			l.EventID, l.CourseID, l.Date, l.Title, l.VideoURL, l.DurationS, l.Summary, author).Scan(&id)
		if err != nil {
			return 0, badRef(err)
		}
		return id, insertSegments(ctx, tx, id, segs)
	})
}

func updateLecture(ctx context.Context, tx pgx.Tx, l Lecture) error {
	return badRef(expectOne(tx.Exec(ctx, `update lectures set event_id = $2, course_id = $3, date = $4::date, title = $5,
		video_url = $6, duration_s = $7, summary = $8
		where id = $1 and deleted_at is null`,
		l.ID, l.EventID, l.CourseID, l.Date, l.Title, l.VideoURL, l.DurationS, l.Summary)))
}

// UpdateLecture changes the lecture's fields and, when segs is not nil,
// replaces its transcript. Reverting such a change restores the fields only.
func (s *Store) UpdateLecture(ctx context.Context, author string, l Lecture, segs []Segment) error {
	_, err := s.write(ctx, author, EntityLecture, ActionUpdate, l.ID, func(tx pgx.Tx) (int64, error) {
		if err := updateLecture(ctx, tx, l); err != nil {
			return 0, err
		}
		if segs == nil {
			return l.ID, nil
		}
		if _, err := tx.Exec(ctx, `delete from lecture_segments where lecture_id = $1`, l.ID); err != nil {
			return 0, err
		}
		return l.ID, insertSegments(ctx, tx, l.ID, segs)
	})
	return err
}

func insertSegments(ctx context.Context, tx pgx.Tx, id int64, segs []Segment) error {
	rows := make([][]any, len(segs))
	for i, sg := range segs {
		rows[i] = []any{id, i, sg.Start, sg.End, sg.Text}
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"lecture_segments"},
		[]string{"lecture_id", "idx", "start_s", "end_s", "text"}, pgx.CopyFromRows(rows))
	return err
}

// Segments returns the transcript pieces that overlap [from, to) seconds; to <= 0 means to the end.
func (s *Store) Segments(ctx context.Context, lectureID int64, from, to int) ([]Segment, error) {
	rows, err := s.db.Query(ctx, `select start_s, end_s, text from lecture_segments
		where lecture_id = $1 and end_s > $2 and ($3 <= 0 or start_s < $3)
		order by idx`, lectureID, from, to)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row pgx.Row) (Segment, error) {
		var sg Segment
		err := row.Scan(&sg.Start, &sg.End, &sg.Text)
		return sg, err
	})
}

// SearchSegments finds transcript pieces containing every word of query or,
// when there are none, any of them: speech recognition misspells some words,
// so a strict match often finds nothing.
func (s *Store) SearchSegments(ctx context.Context, query string, courseID *int64, limit int) ([]SegmentHit, error) {
	words := prefixes(query)
	if len(words) == 0 {
		return []SegmentHit{}, nil
	}
	hits, err := s.searchSegments(ctx, strings.Join(words, " & "), courseID, limit)
	if err != nil || len(hits) > 0 || len(words) == 1 {
		return hits, err
	}
	return s.searchSegments(ctx, strings.Join(words, " | "), courseID, limit)
}

func (s *Store) searchSegments(ctx context.Context, tsq string, courseID *int64, limit int) ([]SegmentHit, error) {
	rows, err := s.db.Query(ctx, `select s.start_s, s.end_s, s.text, l.id, l.course_id, to_char(l.date, 'YYYY-MM-DD'), l.title, l.video_url
		from lecture_segments s join lectures l on l.id = s.lecture_id
		where l.deleted_at is null and ($2::bigint is null or l.course_id = $2)
			and to_tsvector('simple', s.text) @@ to_tsquery('simple', $1)
		order by l.date desc, s.lecture_id, s.idx limit $3`, tsq, courseID, limit)
	if err != nil {
		return nil, err
	}
	return collect(rows, func(row pgx.Row) (SegmentHit, error) {
		var h SegmentHit
		err := row.Scan(&h.Start, &h.End, &h.Text, &h.LectureID, &h.CourseID, &h.Date, &h.Title, &h.VideoURL)
		return h, err
	})
}

// prefixes turns free text into tsquery word prefixes. Long words lose their
// last two letters so that Ukrainian inflections still match («дисертації»
// finds «дисертація»).
func prefixes(q string) []string {
	words := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var parts []string
	for _, w := range words {
		if r := []rune(w); len(r) >= 6 {
			w = string(r[:len(r)-2])
		}
		parts = append(parts, w+":*")
		if len(parts) == 8 {
			break
		}
	}
	return parts
}

// badRef maps a foreign key violation to ErrBadRef.
func badRef(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		return ErrBadRef
	}
	return err
}
