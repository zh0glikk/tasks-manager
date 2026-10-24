package api

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/zh0glikk/tasks-manager/internal/keyauth"
	"github.com/zh0glikk/tasks-manager/internal/ratelimit"
	"github.com/zh0glikk/tasks-manager/internal/store"
)

type keyCtx struct{}

// guard protects writes with a per-IP rate limit and, unless editing is open,
// a bot-issued key with write access in the Authorization header.
func guard(auth keyauth.Verifier, open bool, lim *ratelimit.Limiter, next http.Handler) http.Handler {
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
		if open {
			next.ServeHTTP(w, r)
			return
		}
		k, status, msg := authenticate(r, auth)
		if status != 0 {
			writeJSON(w, status, map[string]any{"error": msg, "login_required": status == http.StatusUnauthorized})
			return
		}
		if !k.CanWrite() {
			fail(w, http.StatusForbidden, "твій ключ дає тільки перегляд; право редагувати дає адмін групи (/grant у боті)")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyCtx{}, k)))
	})
}

// authenticate checks the bearer key; a non-zero status is an HTTP error to return.
func authenticate(r *http.Request, auth keyauth.Verifier) (store.APIKey, int, string) {
	tok, ok := keyauth.Bearer(r)
	if !ok {
		return store.APIKey{}, http.StatusUnauthorized, "увійди з ключем із Telegram-бота, щоб редагувати"
	}
	k, err := auth.Verify(r.Context(), tok)
	if errors.Is(err, keyauth.ErrInvalid) {
		return k, http.StatusUnauthorized, "ключ недійсний або відкликаний; отримай новий у боті: /key"
	}
	if err != nil {
		log.Printf("auth: %v", err)
		return k, http.StatusInternalServerError, "не вдалося перевірити ключ, спробуй пізніше"
	}
	return k, 0, ""
}

func keyFrom(ctx context.Context) (store.APIKey, bool) {
	k, ok := ctx.Value(keyCtx{}).(store.APIKey)
	return k, ok
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
