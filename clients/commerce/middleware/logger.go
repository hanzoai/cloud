package middleware

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

var (
	green   = string([]byte{27, 91, 57, 55, 59, 52, 50, 109})
	white   = string([]byte{27, 91, 57, 48, 59, 52, 55, 109})
	yellow  = string([]byte{27, 91, 57, 55, 59, 52, 51, 109})
	red     = string([]byte{27, 91, 57, 55, 59, 52, 49, 109})
	blue    = string([]byte{27, 91, 57, 55, 59, 52, 52, 109})
	magenta = string([]byte{27, 91, 57, 55, 59, 52, 53, 109})
	cyan    = string([]byte{27, 91, 57, 55, 59, 52, 54, 109})
	reset   = string([]byte{27, 91, 48, 109})
)

func ErrorLogger() gin.HandlerFunc {
	return ErrorLoggerT(gin.ErrorTypeAny)
}

func ErrorLoggerT(typ gin.ErrorType) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		errs := c.Errors.ByType(typ)
		if len(errs) > 0 {
			// -1 status code = do not change current one
			c.JSON(-1, c.Errors)
		}
	}
}

func Log(c *gin.Context) {
	// Start timer
	start := time.Now()

	// Process request
	c.Next()

	// Stop timer
	end := time.Now()
	latency := end.Sub(start)

	method := c.Request.Method
	statusCode := c.Writer.Status()
	statusColor := colorForStatus(statusCode)
	methodColor := colorForMethod(method)

	// Ignore static files
	if strings.Contains(c.Request.URL.Path, "/static/") && c.Writer.Status() < 400 {
		return
	}

	log.Info("%s%3d%s %s%s%s %s %v",
		statusColor, statusCode, reset,
		methodColor, method, reset,
		c.Request.URL.Path,
		latency,
	)
}

func Logger() gin.HandlerFunc {
	return Log
}

func colorForStatus(code int) string {
	switch {
	case code >= 200 && code <= 299:
		return green
	case code >= 300 && code <= 399:
		return white
	case code >= 400 && code <= 499:
		return yellow
	default:
		return red
	}
}

func colorForMethod(method string) string {
	switch {
	case method == "GET":
		return blue
	case method == "POST":
		return cyan
	case method == "PUT":
		return yellow
	case method == "DELETE":
		return red
	case method == "PATCH":
		return green
	case method == "HEAD":
		return magenta
	case method == "OPTIONS":
		return white
	default:
		return reset
	}
}
