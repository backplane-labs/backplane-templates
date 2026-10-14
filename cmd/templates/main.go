// templates is a Backplane tool service: the installation's message
// templates, authored and previewed in the console, rendered for products
// through the templates.Render activity. Products never import it: a
// binding maps their event or hook to templates.Render and on to a
// delivery service.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/backplane-labs/backplane/pkg/backplane"
	"github.com/backplane-labs/backplane/pkg/backplane/config"
	"github.com/backplane-labs/backplane/pkg/backplane/deps"
	"github.com/backplane-labs/backplane/pkg/backplane/drivers/standard"
	"github.com/backplane-labs/backplane/pkg/backplane/infra/postgres"
	"google.golang.org/grpc"

	"github.com/backplane-labs/backplane-templates/internal/admin"
	"github.com/backplane-labs/backplane-templates/internal/store"
	"github.com/backplane-labs/backplane-templates/internal/templates"
	"github.com/backplane-labs/backplane-templates/internal/ui"
	templatesconsolev1 "github.com/backplane-labs/backplane-templates/proto/templates/console/v1"
)

// Config is the whole configuration: file (BACKPLANE_CONFIG_FILE) < env
// TEMPLATES_* (BACKPLANE_* for the SDK block) < Consul KV for Live fields.
type Config struct {
	config.Backplane `json:"backplane"`

	// DefaultLocale: the variant a render falls back to (name@locale); live.
	DefaultLocale config.Live[string] `json:"default_locale" schemapb:"default=en;description=Fallback locale of renders"`
	Store         postgres.Config     `json:"store"`
}

// probeTimeout bounds the store's readiness ping.
const probeTimeout = 2 * time.Second

// State is the service's tree.
type State struct {
	Store     deps.Dependency[*store.Store]
	Templates *templates.Templates
	Admin     *admin.Admin
}

func newState(root backplane.Root[Config]) (*State, error) {
	cfg := root.Config()

	st := &State{Store: deps.NewDependency(root, store.New(cfg.Store), deps.ProbeTimeout(probeTimeout))}

	t, err := templates.New(root, st.Store, templates.Settings{DefaultLocale: cfg.DefaultLocale})
	if err != nil {
		return nil, fmt.Errorf("templates: %w", err)
	}

	st.Templates = t
	st.Admin = admin.New(root, st.Store, t)

	return st, nil
}

func run(ctx context.Context) error {
	svc, err := backplane.Open(ctx, newState, standard.Drivers())
	if err != nil {
		return fmt.Errorf("templates: %w", err)
	}

	// The console module: its internal API, and its bundle when the binary
	// carries one (make web).
	svc.Internal(func(r grpc.ServiceRegistrar) { templatesconsolev1.RegisterAdminServiceServer(r, svc.State().Admin) })

	if bundle := ui.Bundle(); bundle != nil {
		svc.UI(bundle)
	}

	if err := svc.Run(ctx); err != nil {
		return fmt.Errorf("templates: %w", err)
	}

	return nil
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
