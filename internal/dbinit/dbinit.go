// Package dbinit owns publisher's database bootstrap path:
//
//  1. EnsureDatabase — connects to the cluster's `postgres` maintenance
//     database with the same credentials as the application DSN, and
//     issues CREATE DATABASE when the target database is missing.
//
//  2. Migrate — applies any pending up-migrations from the supplied
//     fs.FS (typically migrations.FS) via golang-migrate. Already-applied
//     versions are skipped; new files in subsequent releases are applied
//     in order. golang-migrate takes a PG advisory lock around the run,
//     so concurrent process starts are safe.
//
// Both functions are idempotent: re-running against an already-current
// database is a no-op.
package dbinit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver registration
)

// pgErrDuplicateDatabase is SQLSTATE 42P04, raised when two callers race
// to CREATE the same database. Treated as a successful no-op since the
// post-condition (database exists) holds either way.
const pgErrDuplicateDatabase = "42P04"

// EnsureDatabase creates the database named in dsn if it does not yet
// exist by connecting to the cluster's `postgres` maintenance database.
// Requires the DSN credentials to have CREATEDB privilege only on the
// first run; subsequent starts skip the CREATE entirely.
func EnsureDatabase(ctx context.Context, dsn string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse dsn: %w", err)
	}
	target := cfg.Database
	if target == "" {
		return errors.New("dsn missing database name")
	}
	if !validIdent(target) {
		return fmt.Errorf("invalid database identifier %q (allow [A-Za-z0-9_], <=63 chars)", target)
	}

	cfg.Database = "postgres"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect maintenance db: %w", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, target,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check db existence: %w", err)
	}
	if exists {
		return nil
	}
	// CREATE DATABASE cannot use parameter binding for the identifier;
	// validIdent above is the injection guard.
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, target)); err != nil {
		// 42P04 means a concurrent starter beat us to the CREATE; either
		// way the database now exists, so swallow it.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgErrDuplicateDatabase {
			return nil
		}
		return fmt.Errorf("create database %q: %w", target, err)
	}
	return nil
}

// Migrate applies pending up-migrations from files against dsn. files is
// typically migrations.FS embedded in the binary. Returns nil when the
// database is already at the latest version.
func Migrate(ctx context.Context, dsn string, files fs.FS) error {
	src, err := iofs.New(files, ".")
	if err != nil {
		return fmt.Errorf("migration source: %w", err)
	}
	defer src.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open pgx: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}

	drv, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		return fmt.Errorf("migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx", drv)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// validIdent restricts CREATE DATABASE identifiers to a safe subset since
// PG does not bind identifiers as parameters. Matches Postgres' default
// 63-char NAMEDATALEN limit.
func validIdent(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for _, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
