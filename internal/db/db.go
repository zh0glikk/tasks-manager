package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// appSchema is used instead of public when the database user can't create
// tables in public (DigitalOcean dev databases on PostgreSQL 15+).
const appSchema = "app"

// Connect builds the pool without dialing; the first query (Migrate) surfaces
// connection errors and is retried by the caller.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	// A schema missing from search_path is skipped, so this resolves to public
	// until (and unless) Migrate has to create appSchema.
	cfg.ConnConfig.RuntimeParams["search_path"] = appSchema + ",public"
	return pgxpool.NewWithConfig(ctx, cfg)
}

func ensureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var canPublic bool
	if err := pool.QueryRow(ctx, `select has_schema_privilege(current_user, 'public', 'CREATE')`).Scan(&canPublic); err != nil {
		return err
	}
	if canPublic {
		return nil
	}
	if _, err := pool.Exec(ctx, `create schema if not exists `+appSchema); err != nil {
		return fmt.Errorf("no CREATE privilege on schema public and can't create schema %s: %w", appSchema, err)
	}
	log.Printf("no CREATE privilege on schema public, using schema %q", appSchema)
	return nil
}

// Migrate applies embedded migrations in filename order, each in its own transaction.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if err := ensureSchema(ctx, pool); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `create table if not exists schema_migrations (
		version text primary key,
		applied_at timestamptz not null default now()
	)`); err != nil {
		return err
	}

	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	for _, name := range names {
		var exists bool
		if err := pool.QueryRow(ctx, `select exists(select 1 from schema_migrations where version = $1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `insert into schema_migrations (version) values ($1)`, name); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
