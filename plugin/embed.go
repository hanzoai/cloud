package plugin

import (
	"embed"
	"sync"
)

// specs holds every OSS app's openapi.json projected from its own router.
//
//go:embed */openapi.json
var specs embed.FS

var (
	specMu        sync.RWMutex
	specProviders []func(app string) []byte
)

// RegisterSpec registers an additional OpenAPI subset lookup (checked in LIFO
// order ahead of the embedded OSS specs). Private distributions use this to
// supply subsets for private plugins or overridden mounts without copying
// plugin/ or cmd/cloud/host.
func RegisterSpec(fn func(app string) []byte) {
	if fn == nil {
		return
	}
	specMu.Lock()
	specProviders = append(specProviders, fn)
	specMu.Unlock()
}

// Spec returns app's OpenAPI subset JSON, or nil for an app that ships none.
func Spec(app string) []byte {
	specMu.RLock()
	providers := specProviders
	specMu.RUnlock()
	for i := len(providers) - 1; i >= 0; i-- {
		if b := providers[i](app); len(b) > 0 {
			return b
		}
	}
	b, err := specs.ReadFile(app + "/openapi.json")
	if err != nil {
		return nil
	}
	return b
}
