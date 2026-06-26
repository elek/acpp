package cli

import (
	"context"
	"log/slog"

	"github.com/elek/acpp/config"
	"github.com/elek/acpp/db"
	"github.com/pkg/errors"
)

// openStore opens the session store backing the web UI. When a database DSN is
// configured it connects to PostgreSQL; otherwise it falls back to an in-memory
// store so the web UI works out of the box (live sessions and their history are
// kept only for the lifetime of the process).
func openStore(ctx context.Context, cfg *config.Config) (db.Store, error) {
	if cfg.Database.DSN == "" {
		slog.Warn("no database dsn configured, using in-memory store (history is not persisted)")
		return db.NewMemStore(), nil
	}
	store, err := db.Connect(ctx, cfg.Database.DSN)
	if err != nil {
		return nil, errors.Wrap(err, "database connection failed")
	}
	return store, nil
}
