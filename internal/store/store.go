// Package store is the service's PostgreSQL: the SDK's postgres provider
// with the service's schema (templates) and migrations, plus the queries
// sqld generated.
package store

import (
	"context"
	"embed"

	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"

	"github.com/gopherex/pgtx/pkg/tx"

	"github.com/backplane-labs/backplane-templates/internal/store/db"
)

// Schema every table lives in.
const Schema = "templates"

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the connection with the typed queries.
type Store struct {
	*postgres.DB

	// Q runs on DB: inside the context's transaction, else the pool.
	Q *db.Queries
}

// New is the provider: the postgres driver with the schema and the
// migrations.
func New(cfg postgres.Config) deps.Provider[*Store] {
	return provider{inner: postgres.New(cfg, postgres.Schema(Schema), postgres.Migrations(migrations))}
}

type provider struct {
	inner deps.Provider[*postgres.DB]
}

func (provider) Name() string { return "store" }

func (p provider) Provide(ctx context.Context, s deps.Scope) (*Store, error) {
	pg, err := p.inner.Provide(ctx, s)
	if err != nil {
		return nil, err //nolint:wrapcheck // the driver's own error
	}

	return &Store{DB: pg, Q: db.New(pg.DB)}, nil
}

func (p provider) Probe(ctx context.Context, st *Store) error {
	return p.inner.Probe(ctx, st.DB) //nolint:wrapcheck // the driver's own error
}

func (p provider) Close(ctx context.Context, st *Store) error {
	return p.inner.Close(ctx, st.DB) //nolint:wrapcheck // the driver's own error
}

// InTx runs fn in one read-committed transaction; queries on Q join it.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return tx.DoReadCommitted(ctx, s.Trm, fn) //nolint:wrapcheck // fn's own error
}
