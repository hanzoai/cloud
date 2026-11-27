package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/source"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the machines app (/v1/machines — linked personal
// machines + cloud compute VMs/GPUs).
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "machines",
		Price:    cloud.Free,
		Use:      source.UseMachines,
		Shutdown: source.Shutdown,
	}}, []string{"machines"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
