package color

import (
	"io"

	. "github.com/op/go-logging"
)

var (
	colors = []string{
		CRITICAL: "\033[35m",
		ERROR:    "\033[31m",
		WARNING:  "\033[33m",
		NOTICE:   "\033[32m",
		INFO:     "\033[37m",
		DEBUG:    "\033[36m",
	}
	boldcolors = []string{
		CRITICAL: "\033[1;35m",
		ERROR:    "\033[1;31m",
		WARNING:  "\033[1;33m",
		NOTICE:   "\033[1;32m",
		INFO:     "\033[37m",
		DEBUG:    "\033[1;36m",
	}
)

func FmtVerbLevel(layout string, level Level, output io.Writer) {
	if layout == "bold" {
		output.Write([]byte(boldcolors[level]))
	} else if layout == "reset" {
		output.Write([]byte("\033[0m"))
	} else {
		output.Write([]byte(colors[level]))
	}
}
