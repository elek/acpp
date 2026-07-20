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
//
// Every daemon that owns sessions (`acpp web`, `acpp serve`) opens the store
// here, so this is where stale sessions from a previous run are finalized: any
// conversation still marked running/pending belongs to a process that exited
// without finalizing it (a crash or kill -9). Completing them on startup keeps
// stale sessions from lingering as active — for example lighting up every
// project green in the web UI after a restart. Doing it in openStore (rather
// than each command) means no daemon can forget to.
func openStore(ctx context.Context, cfg *config.Config) (db.Store, error) {
	if cfg.Database.DSN == "" {
		slog.Warn("no database dsn configured, using in-memory store (history is not persisted)")
		return db.NewMemStore(), nil
	}
	store, err := db.Connect(ctx, cfg.Database.DSN)
	if err != nil {
		return nil, errors.Wrap(err, "database connection failed")
	}
	if n, err := store.CompleteRunningSessions(ctx); err != nil {
		store.Close()
		return nil, errors.Wrap(err, "completing stale sessions from previous run")
	} else if n > 0 {
		slog.Info("marked stale sessions from previous run as complete", "count", n)
	}
	return store, nil
}
