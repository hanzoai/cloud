package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/train"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the train app (HIP-1333).
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "train",
		Price:    cloud.Metered,
		Use:      train.Use,
		Shutdown: cloud.CtxShutdown(train.Shutdown),
	}}, []string{"train"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
