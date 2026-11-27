package main

import (
	"fmt"
	"github.com/hanzoai/cloud/apps/worlds"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
	"os"
)

func main() {
	if err := cloud.Listen([]cloud.Plugin{{Name: "worlds", Price: cloud.Free, Use: worlds.Use}}, []string{"worlds"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
