// Package integration holds end-to-end tests that exercise the router against a
// real PostgreSQL database and a real agent subprocess (rai acp fake). The tests
// are gated on the ACPP_TEST_POSTGRES environment variable: when it is unset they
// skip, so `go test ./...` stays green for developers without a test database.
package integration

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/elek/acpp/db"
	"github.com/elek/acpp/persistence"
	"github.com/elek/acpp/router"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// DSNEnv is the environment variable holding the test database connection
// string, e.g. postgres://acpp:acpp@localhost:5433/acpp?sslmode=disable.
const DSNEnv = "ACPP_TEST_POSTGRES"

// WithRouter sets up an isolated integration environment and runs fn against it.
//
// It connects to the test database named by ACPP_TEST_POSTGRES (skipping the
// test when unset), runs the schema migrations (db.Connect does this on connect),
// truncates all content so each test starts from an empty database, creates a
// temporary directory to hold project working trees, and starts a router wired to
// the database via the persistence subscriber.
//
// fn receives the temporary projects directory and the live router. All resources
// are released via t.Cleanup when the test finishes.
func WithRouter(t *testing.T, fn func(t *testing.T, dir string, r *router.Router)) {
	t.Helper()

	dsn := envOrSkip(t)
	ctx := context.Background()

	store, err := db.Connect(ctx, dsn)
	require.NoError(t, err, "connecting to test database (and running migrations)")
	t.Cleanup(store.Close)

	cleanContent(t, ctx, dsn)

	dir := t.TempDir()

	r := router.New()
	t.Cleanup(r.Close)

	// Wire database persistence; New subscribes the persister to the router.
	persistence.New(r, store)

	fn(t, dir, r)
}

// envOrSkip returns the test DSN, or skips the test when it is not configured.
func envOrSkip(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(DSNEnv))
	if dsn == "" {
		t.Skipf("%s not set; skipping integration test", DSNEnv)
	}
	return dsn
}

// cleanContent truncates every table in the public schema except goose's
// migration bookkeeping, so each test observes an empty database. Identity
// sequences are reset and foreign keys are handled via CASCADE.
func cleanContent(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "opening pool for content cleanup")
	defer pool.Close()

	rows, err := pool.Query(ctx,
		`SELECT tablename FROM pg_tables
		 WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`)
	require.NoError(t, err, "listing tables")

	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, `"`+name+`"`)
	}
	require.NoError(t, rows.Err())
	rows.Close()

	if len(tables) == 0 {
		return
	}

	stmt := "TRUNCATE TABLE " + strings.Join(tables, ", ") + " RESTART IDENTITY CASCADE"
	_, err = pool.Exec(ctx, stmt)
	require.NoError(t, err, "truncating tables")
}
