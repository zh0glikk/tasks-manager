package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// guard protects writes: a per-IP rate limit (which also caps edit-code
// guessing) and, when an edit code is configured, a matching X-Edit-Code header.
func guard(editCode string, lim *limiter, next http.Handler) http.Handler {
	editCode = normalizeCode(editCode)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if ok, retry := lim.allow(clientIP(r)); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			fail(w, http.StatusTooManyRequests, "забагато змін за хвилину, спробуйте трохи згодом")
			return
		}
		if editCode != "" && subtle.ConstantTimeCompare([]byte(normalizeCode(r.Header.Get("X-Edit-Code"))), []byte(editCode)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "неправильний код групи", "code_required": true})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func normalizeCode(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// clientIP prefers the address set by the hosting proxy.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("Do-Connecting-Ip"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limiter is a fixed-window counter per key.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func newLimiter(max int, per time.Duration) *limiter {
	return &limiter{max: max, window: per, hits: map[string]*window{}}
}

func (l *limiter) allow(key string) (bool, time.Duration) {
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
