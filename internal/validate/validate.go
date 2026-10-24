// Package validate normalizes and checks entities before they are stored.
// Errors are user-facing messages in Ukrainian.
package validate

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zh0glikk/tasks-manager/internal/store"
)

var (
	reDate  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reTime  = regexp.MustCompile(`^\d{2}:\d{2}$`)
	reColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

func Course(c *store.Course) error {
	trim(&c.Name, &c.Short, &c.Teacher, &c.Link, &c.Color)
	if c.Color == "" {
		c.Color = "#0E5D69"
	}
	return first(
		rule(c.Name != "", "вкажіть назву предмета"),
		rule(c.Short != "", "вкажіть коротку назву"),
		maxLen(c.Name, 200, "назва"), maxLen(c.Short, 30, "коротка назва"), maxLen(c.Teacher, 200, "викладач"),
		link(c.Link),
		rule(reColor.MatchString(c.Color), "колір у форматі #RRGGBB"),
	)
}

func Event(e *store.Event) error {
	trim(&e.Kind, &e.Date, &e.StartTime, &e.EndTime, &e.Teacher, &e.Link, &e.Note)
	return first(
		rule(Date(e.Date), "вкажіть дату у форматі YYYY-MM-DD"),
		rule(reTime.MatchString(e.StartTime), "вкажіть час початку у форматі HH:MM"),
		rule(e.EndTime == "" || reTime.MatchString(e.EndTime), "неправильний час закінчення"),
		rule(e.EndTime == "" || e.EndTime > e.StartTime, "кінець має бути пізніше початку"),
		maxLen(e.Kind, 40, "тип"), maxLen(e.Teacher, 200, "викладач"), maxLen(e.Note, 1000, "примітка"),
		link(e.Link),
	)
}

func Assignment(a *store.Assignment) error {
	trim(&a.Title, &a.Description, &a.DueDate, &a.DueTime, &a.Link)
	return first(
		rule(a.Title != "", "вкажіть, що треба зробити"),
		maxLen(a.Title, 200, "назва"), maxLen(a.Description, 5000, "деталі"),
		rule(Date(a.DueDate), "вкажіть дату дедлайну у форматі YYYY-MM-DD"),
		rule(a.DueTime == "" || reTime.MatchString(a.DueTime), "неправильний час, потрібно HH:MM"),
		link(a.Link),
	)
}

func Post(p *store.Post) error {
	trim(&p.Title, &p.Body)
	return first(
		rule(p.Title != "", "вкажіть заголовок"),
		maxLen(p.Title, 200, "заголовок"), maxLen(p.Body, 5000, "текст"),
	)
}

// Date reports whether s is a real calendar date in YYYY-MM-DD form.
func Date(s string) bool {
	if !reDate.MatchString(s) {
		return false
	}
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func rule(ok bool, msg string) error {
	if ok {
		return nil
	}
	return errors.New(msg)
}

func maxLen(s string, n int, field string) error {
	return rule(utf8.RuneCountInString(s) <= n, field+": не більше "+strconv.Itoa(n)+" символів")
}

func link(s string) error {
	return rule(s == "" || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://"), "посилання має починатися з https://")
}

func first(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func trim(ss ...*string) {
	for _, s := range ss {
		*s = strings.TrimSpace(*s)
	}
}
