package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/ai"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
)

func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "ai",
		Price:    cloud.Metered,
		Root:     ai.Use,
		Shutdown: ai.Shutdown,
	}}, []string{"ai"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
