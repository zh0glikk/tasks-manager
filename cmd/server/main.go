package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zh0glikk/tasks-manager/internal/api"
	"github.com/zh0glikk/tasks-manager/internal/db"
	"github.com/zh0glikk/tasks-manager/internal/store"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer pool.Close()

	// Migrations retry in the background so the server (and its health check)
	// comes up even if the database isn't writable yet — e.g. a DigitalOcean
	// dev database only grants schema access after the first healthy deploy.
	var ready atomic.Bool
	go migrateUntilDone(ctx, pool, &ready)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           waitReady(&ready, api.New(store.New(pool), web.Static())),
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

func migrateUntilDone(ctx context.Context, pool *pgxpool.Pool, ready *atomic.Bool) {
	delay := 2 * time.Second
	for {
		err := db.Migrate(ctx, pool)
		if err == nil {
			log.Print("migrations applied")
			ready.Store(true)
			return
		}
		log.Printf("migrate: %v (retrying in %s)", err, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, time.Minute)
	}
}

// waitReady answers API calls with 503 until migrations have been applied.
func waitReady(ready *atomic.Bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() && strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":"сервер запускається, спробуйте за хвилину"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
