package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zh0glikk/tasks-manager/internal/api"
	"github.com/zh0glikk/tasks-manager/internal/db"
	"github.com/zh0glikk/tasks-manager/internal/keyauth"
	"github.com/zh0glikk/tasks-manager/internal/mcpserver"
	"github.com/zh0glikk/tasks-manager/internal/store"
	"github.com/zh0glikk/tasks-manager/internal/telegram"
	"github.com/zh0glikk/tasks-manager/web"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		log.Fatalf("timezone: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer pool.Close()
	st := store.New(pool)

	if len(os.Args) > 1 && os.Args[1] == "keys" {
		if err := db.Migrate(ctx, pool); err != nil {
			log.Fatalf("migrate: %v", err)
		}
		os.Exit(keysCommand(ctx, st, os.Args[2:]))
	}

	var bot *telegram.Bot
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		bot = telegram.New(telegram.Config{
			Token:     token,
			APIURL:    os.Getenv("TELEGRAM_API_URL"),
			ChatID:    envInt("TELEGRAM_CHAT_ID"),
			Admins:    envInts("TELEGRAM_ADMIN_IDS"),
			PublicURL: os.Getenv("PUBLIC_URL"),
			Retention: time.Duration(envIntDefault("CHAT_RETENTION_DAYS", 14)) * 24 * time.Hour,
			Location:  loc,
		}, st)
	} else {
		log.Print("TELEGRAM_BOT_TOKEN not set, Telegram bot disabled")
	}

	// Migrations retry in the background so the server (and its health check)
	// comes up even if the database isn't writable yet — e.g. a DigitalOcean
	// dev database only grants schema access after the first healthy deploy.
	var ready atomic.Bool
	go func() {
		if !migrateUntilDone(ctx, pool, &ready) || bot == nil {
			return
		}
		log.Print("telegram bot started")
		bot.Run(ctx)
	}()

	deps := mcpserver.Deps{Store: st, Location: loc}
	verifier := keyauth.Verifier{Store: st}
	if bot != nil {
		deps.Members = bot
		verifier.Members = bot
	}
	open := os.Getenv("OPEN_EDITING") == "true"
	if open {
		log.Print("OPEN_EDITING=true: anyone can edit the site without a key")
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.Handler(deps))
	apiOpts := api.Options{Auth: verifier, Open: open}
	if bot != nil {
		apiOpts.BotUsername = bot.Username
	}
	mux.Handle("/", api.New(st, web.Static(), apiOpts))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           waitReady(&ready, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

// migrateUntilDone retries migrations with backoff; false means ctx ended first.
func migrateUntilDone(ctx context.Context, pool *pgxpool.Pool, ready *atomic.Bool) bool {
	delay := 2 * time.Second
	for {
		err := db.Migrate(ctx, pool)
		if err == nil {
			log.Print("migrations applied")
			ready.Store(true)
			return true
		}
		log.Printf("migrate: %v (retrying in %s)", err, delay)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
		delay = min(delay*2, time.Minute)
	}
}

// waitReady answers API and MCP calls with 503 until migrations have been applied.
func waitReady(ready *atomic.Bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() && (strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/mcp") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":"сервер запускається, спробуйте за хвилину"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func envInt(name string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64)
	return n
}

func envIntDefault(name string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envInts(name string) []int64 {
	var out []int64
	for _, s := range strings.Split(os.Getenv(name), ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}
