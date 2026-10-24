// Package ratelimit is a small in-memory fixed-window limiter keyed by string.
package ratelimit

import (
	"sync"
	"time"
)

type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func New(max int, per time.Duration) *Limiter {
	return &Limiter{max: max, window: per, hits: map[string]*window{}}
}

// Allow counts one hit for key. When the limit is reached it returns false and
// the time until the window resets.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	w := l.hits[key]
	if w == nil || now.Sub(w.start) >= l.window {
		if len(l.hits) > 10000 {
			for k, v := range l.hits {
				if now.Sub(v.start) >= l.window {
					delete(l.hits, k)
				}
			}
		}
		l.hits[key] = &window{start: now, n: 1}
		return true, 0
	}
	if w.n >= l.max {
		return false, l.window - now.Sub(w.start)
	}
	w.n++
	return true, 0
}
