package cloud

import (
	"fmt"

	luxlog "github.com/luxfi/log"
)

// BuildDeps constructs the Deps used by every subsystem's Mount(app, deps).
// For each enabled subsystem, the corresponding Client field gets a real
// in-process implementation; disabled subsystems get a ZAP-RPC client
// pointing at an external endpoint (if configured) or a nil client (if
// not used).
//
// In this initial scaffold the clients are nil — concrete in-process
// wiring lands as each subsystem's Mount() is integrated. Subsystems
// should defensively handle deps.X == nil during the rollout.
func BuildDeps(cfg *Config) Deps {
	logger := luxlog.New("cloud")
	logger.Info("building deps",
		"brand", cfg.Brand,
		"domain", cfg.Domain,
		"data_dir", cfg.DataDir,
		"enabled", cfg.Enable,
	)
	return Deps{
		Logger:  logger,
		Brand:   cfg.Brand,
		Domain:  cfg.Domain,
		DataDir: cfg.DataDir,
		// Subsystem clients are populated incrementally by subsystem Init
		// hooks during the migration. See cmd/cloud/main.go.
	}
}

// MountFunc is the canonical signature every subsystem exposes per
// HIP-0106. Each Hanzo Go service ships a top-level `Mount` symbol
// matching this signature; cmd/cloud/main.go imports the package and
// calls it.
type MountFunc func(app any, deps Deps) error // app is *zip.App; using any here to avoid an import cycle in pkg/cloud

// MountSpec describes one subsystem registered for mounting. The Order
// is used when ordering matters for inter-subsystem deps (e.g. iam
// before authz before commerce).
type MountSpec struct {
	Name  string
	Order int
	Mount MountFunc
}

// Registry is the in-process subsystem registry. Subsystems register via
// init() functions in their respective packages OR cmd/cloud/main.go can
// explicitly enumerate them. Either pattern works.
var Registry []MountSpec

// Register adds a subsystem to the in-process registry.
func Register(name string, order int, mount MountFunc) {
	Registry = append(Registry, MountSpec{Name: name, Order: order, Mount: mount})
}

// MountAll iterates the registry in order and calls Mount() on each
// enabled subsystem.
func MountAll(app any, cfg *Config, deps Deps) error {
	// Sort registry by order — bubble sort, registry is tiny.
	for i := 0; i < len(Registry); i++ {
		for j := i + 1; j < len(Registry); j++ {
			if Registry[j].Order < Registry[i].Order {
				Registry[i], Registry[j] = Registry[j], Registry[i]
			}
		}
	}

	logger := deps.Logger
	for _, spec := range Registry {
		if !cfg.Enabled(spec.Name) {
			logger.Debug("subsystem disabled", "name", spec.Name)
			continue
		}
		if err := spec.Mount(app, deps); err != nil {
			return fmt.Errorf("mount %s: %w", spec.Name, err)
		}
		logger.Info("mounted subsystem", "name", spec.Name)
	}
	return nil
}
