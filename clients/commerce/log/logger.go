package log

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/op/go-logging"
)

// Custom logger
type Logger struct {
	logging.Logger
	backend          *Backend
	verbose          bool
	verboseRequested bool
}

func (l *Logger) SetVerbose(verbose bool) {
	l.verbose = verbose
}

func (l *Logger) Verbose() bool {
	return l.verboseRequested || std.verbose
}

// Check if we have been passed a gin context
func (l *Logger) detectContext(ctx interface{}) {
	l.verboseRequested = false

	switch ctx := ctx.(type) {
	case *gin.Context:
		// Get request context from session
		if reqCtx, exists := ctx.Get("context"); exists {
			l.backend.context = reqCtx.(context.Context)
		} else {
			l.backend.context = ctx.Request.Context()
		}
		// The "verbose" key is only set by middleware/overrides; routes that
		// don't run it (e.g. the gateway/IAM-authenticated billing handlers)
		// would otherwise panic here on MustGet. Default to false when absent.
		if v, ok := ctx.Get("verbose"); ok {
			if b, ok := v.(bool); ok {
				l.verboseRequested = b
			}
		}

		// Request URI is useful for logging
		if ctx.Request != nil {
			l.backend.requestURI = ctx.Request.RequestURI
		}
	case context.Context:
		l.backend.context = ctx
	default:
		l.backend.context = nil
	}
}

// Check if error was passed as last argument
func (l *Logger) detectError(args []interface{}) {
	if len(args) > 0 {
		if err, ok := args[len(args)-1].(error); ok {
			l.backend.error = err
		}
	}
}

// Grab last object (presumably to dump)
func (l *Logger) dumpObject(args []interface{}) ([]interface{}, interface{}) {
	if len(args) > 0 {
		// Grab last argument
		last := args[len(args)-1]
		// Remove from args
		args = args[:len(args)-1]
		return args, last
	}
	return args, nil
}

// Process args, setting context if passed one.
func (l *Logger) parseArgs(args ...interface{}) []interface{} {
	if len(args) == 0 {
		return args
	}

	// Check if we have been passed a Gin context
	l.detectContext(args[len(args)-1])

	// Remove context from args if we were passed one
	if l.backend.context != nil {
		args = args[:len(args)-1]
	}

	// Last non-context argument might be an error.
	l.detectError(args)

	return args
}
