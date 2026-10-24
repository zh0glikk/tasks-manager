package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/zh0glikk/tasks-manager/internal/ratelimit"
)

// guard protects writes: a per-IP rate limit (which also caps edit-code
// guessing) and, when an edit code is configured, a matching X-Edit-Code header.
func guard(editCode string, lim *ratelimit.Limiter, next http.Handler) http.Handler {
	editCode = normalizeCode(editCode)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if ok, retry := lim.Allow(clientIP(r)); !ok {
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
