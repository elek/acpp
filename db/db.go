package db

import (
	"context"
	"database/sql"
	"embed"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pkg/errors"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the top-level interface combining all database operations.
type Store interface {
	SessionWriter
	SessionReader
	ProjectStore
	Close()
}

// PostgresStore provides database operations backed by PostgreSQL.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// Connect creates a new PostgresStore by connecting to PostgreSQL and running migrations.
func Connect(ctx context.Context, dsn string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, errors.Wrap(err, "creating connection pool")
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.Wrap(err, "pinging database")
	}

	if err := runMigrations(pool); err != nil {
		pool.Close()
		return nil, errors.Wrap(err, "running migrations")
	}

	return &PostgresStore{pool: pool}, nil
}

// Close closes the connection pool.
func (s *PostgresStore) Close() {
	s.pool.Close()
}

func runMigrations(pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrations)

	db := stdlib.OpenDBFromPool(pool)
	defer func(db *sql.DB) {
		_ = db.Close()
	}(db)

	if err := goose.SetDialect("postgres"); err != nil {
		return errors.Wrap(err, "setting dialect")
	}

	if err := goose.Up(db, "migrations"); err != nil {
		return errors.Wrap(err, "running up migrations")
	}

	return nil
}
