package exec

import (
	"github.com/hanzoai/cloud/clients/commerce/config"
	"log"
	"os"
	"os/exec"
	"strings"
)

var conf = config.Get()

func Run(cmd string) {
	args := strings.Split(cmd, " ")
	cmd, args = args[0], args[1:]

	// get old path
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")

	proc := exec.Command(cmd, args...)
	proc.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	proc.Dir = conf.RootDir
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	if err := proc.Run(); err != nil {
		log.Fatalln(err)

	}

	// Reset $PATH
	os.Setenv("PATH", oldPath)
}
