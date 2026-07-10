package log

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/op/go-logging"

	"github.com/hanzoai/cloud/clients/commerce/config"
)

// LogLevel represents log severity levels
type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarning
	LevelError
	LevelCritical
)

// Custom logger backend that writes to stdout/stderr
type Backend struct {
	context    context.Context
	error      error
	requestURI string
	verbose    bool
	logger     *log.Logger
	errLogger  *log.Logger
}

func (b Backend) Verbose() bool {
	return b.verbose
}

// NewBackend creates a new logging backend
func NewBackend(ctx context.Context) *Backend {
	return &Backend{
		context:   ctx,
		verbose:   config.IsDevelopment,
		logger:    log.New(os.Stdout, "", log.LstdFlags),
		errLogger: log.New(os.Stderr, "", log.LstdFlags),
	}
}

// logToStdout writes log messages to stdout/stderr based on level
func (b Backend) logToStdout(level logging.Level, formatted string) error {
	prefix := levelPrefix(level)
	message := fmt.Sprintf("%s %s", prefix, formatted)

	switch level {
	case logging.ERROR, logging.CRITICAL:
		b.errLogger.Println(message)
	default:
		b.logger.Println(message)
	}

	return nil
}

// levelPrefix returns a string prefix for the given log level
func levelPrefix(level logging.Level) string {
	switch level {
	case logging.DEBUG:
		return "[DEBUG]"
	case logging.INFO:
		return "[INFO]"
	case logging.WARNING:
		return "[WARN]"
	case logging.ERROR:
		return "[ERROR]"
	case logging.CRITICAL:
		return "[CRITICAL]"
	default:
		return "[LOG]"
	}
}

// Log method that outputs to stdout/stderr
func (b Backend) Log(level logging.Level, calldepth int, record *logging.Record) error {
	// Create formatted log output
	formatted := record.Formatted(calldepth + 2)

	// Always log to stdout/stderr
	return b.logToStdout(level, formatted)
}
