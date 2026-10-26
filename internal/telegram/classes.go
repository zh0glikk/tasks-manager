package telegram

import (
	"context"
	"fmt"
	"html"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/zh0glikk/tasks-manager/internal/store"
)

const (
	classLead    = 10 * time.Minute // first reminder this long before a class
	classCheck   = 15 * time.Second
	classLate    = 2 * time.Minute // a missed moment still counts this long after
	classHorizon = 400             // days of schedule kept in memory
)

// classCache keeps the schedule in memory so the frequent check never
// queries the database. It is loaded on first use and reloaded only after the
// store reports a change to classes or courses.
type classCache struct {
	mu      sync.Mutex
	stale   bool
	events  []store.Event
	courses map[int64]store.Course
	sent    map[string]bool // event id + moment + start, so a moved class reminds again
}

func (c *classCache) invalidate(entity string) {
	switch entity {
	case store.EntityEvent, store.EntitySeries, store.EntityCourse:
		c.mu.Lock()
		c.stale = true
		c.mu.Unlock()
	}
}

// classReminders goes to the admins for now.
func (b *Bot) classReminders(ctx context.Context) {
	if len(b.admins) == 0 {
		return
	}
	tick := time.NewTicker(classCheck)
	defer tick.Stop()
	for {
		b.CheckClasses(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// CheckClasses sends the reminders due at now: classLead before a class and
// when it starts. Exported for tests.
func (b *Bot) CheckClasses(ctx context.Context, now time.Time) {
	c := &b.classes
	c.mu.Lock()
	if c.stale {
		if err := b.loadClasses(ctx, now); err != nil {
			c.mu.Unlock()
			log.Printf("telegram: load classes: %v", err)
			return
		}
	}
	var due []string
	for _, e := range c.events {
		start, err := time.ParseInLocation("2006-01-02 15:04", e.Date+" "+e.StartTime, b.cfg.Location)
		if err != nil {
			continue
		}
		for _, m := range []struct {
			name string
			at   time.Time
		}{{"soon", start.Add(-classLead)}, {"start", start}} {
			key := strconv.FormatInt(e.ID, 10) + " " + m.name + " " + e.Date + " " + e.StartTime
			if now.Before(m.at) || now.Sub(m.at) >= classLate || c.sent[key] {
				continue
			}
			c.sent[key] = true
			due = append(due, b.classText(e, c.courses, m.name == "soon"))
		}
	}
	c.mu.Unlock()

	for _, text := range due {
		b.notifyAdmins(ctx, text)
	}
}

// loadClasses reads the schedule from today on; c.mu must be held.
func (b *Bot) loadClasses(ctx context.Context, now time.Time) error {
	day := now.In(b.cfg.Location)
	from := day.Format(time.DateOnly)
	to := day.AddDate(0, 0, classHorizon).Format(time.DateOnly)
	events, err := b.store.ListEvents(ctx, from, to)
	if err != nil {
		return err
	}
	courses, err := b.store.ListCourses(ctx)
	if err != nil {
		return err
	}
	c := &b.classes
	c.events, c.courses, c.stale = events, map[int64]store.Course{}, false
	for _, cr := range courses {
		c.courses[cr.ID] = cr
	}
	log.Printf("telegram: loaded %d classes for reminders", len(events))
	return nil
}

func (b *Bot) classText(e store.Event, courses map[int64]store.Course, soon bool) string {
	cr, ok := courses[0], false
	if e.CourseID != nil {
		cr, ok = courses[*e.CourseID]
	}
	name := "Заняття"
	if ok {
		name = cr.Name
	}
	when := "▶️ <b>Почалась пара</b>"
	if soon {
		when = fmt.Sprintf("⏰ <b>Через %d хв пара</b>", int(classLead.Minutes()))
	}
	tm := e.StartTime
	if e.EndTime != "" {
		tm += "–" + e.EndTime
	}
	text := fmt.Sprintf("%s\n%s · %s, %s", when, html.EscapeString(name), html.EscapeString(e.Kind), tm)
	if e.Tentative {
		text += " (орієнтовно)"
	}
	teacher := e.Teacher
	if teacher == "" {
		teacher = cr.Teacher
	}
	if teacher != "" {
		text += "\n" + html.EscapeString(teacher)
	}
	if e.Note != "" {
		text += "\n" + html.EscapeString(e.Note)
	}
	link := e.Link
	if link == "" {
		link = cr.Link
	}
	if link != "" {
		text += "\n" + html.EscapeString(link)
	}
	return text
}
