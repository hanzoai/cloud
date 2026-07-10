package log

import (
	"fmt"
	"net/http"
	"net/http/httputil"

	"github.com/op/go-logging"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/spew"
)

// Create a new logger
func New() *Logger {
	log := new(Logger)
	log.backend = NewBackend(nil)

	// Log formatters, color for dev, plain for production
	plainFormatter := MustStringFormatter("%{longfile} %{longfunc} %{message}")
	colorFormatter := MustStringFormatter("%{color}%{level:.5s} %{longfile} %{longfunc} %{color:reset}%{message}")

	// Use plain formatter for production logging, color for dev server
	backend := logging.NewBackendFormatter(log.backend, plainFormatter)
	if !config.IsProduction {
		backend = logging.NewBackendFormatter(backend, colorFormatter)
	}

	log.SetBackend(logging.SetBackend(backend))

	if config.IsDevelopment {
		log.SetVerbose(true)
	}

	return log
}

var std = New()

func SetVerbose(verbose bool) {
	std.SetVerbose(verbose)
}

func Verbose() bool {
	return std.Verbose()
}

func Debug(formatOrError interface{}, args ...interface{}) {
	args = std.parseArgs(args...)

	if !std.Verbose() {
		return
	}

	switch v := formatOrError.(type) {
	case error:
		std.Debugf(errAndStack(v))
	case string:
		std.Debugf(v, args...)
	}
}

func Info(formatOrError interface{}, args ...interface{}) {
	args = std.parseArgs(args...)

	switch v := formatOrError.(type) {
	case error:
		std.Infof(errAndStack(v))
	case string:
		std.Infof(v, args...)
	}
}

func Warn(formatOrError interface{}, args ...interface{}) {
	args = std.parseArgs(args...)

	switch v := formatOrError.(type) {
	case error:
		std.Warningf(errAndStack(v))
	case string:
		std.Warningf(v, args...)
	}
}

func Error(formatOrError interface{}, args ...interface{}) error {
	args = std.parseArgs(args...)

	switch v := formatOrError.(type) {
	case error:
		std.Errorf(errAndStack(v))
		fmt.Println(v)
		return v
	case string:
		std.Errorf(v, args...)
		fmt.Println(v)
		return fmt.Errorf(v, args...)
	}
	return nil
}

func Fatal(formatOrError interface{}, args ...interface{}) {
	args = std.parseArgs(args...)

	switch v := formatOrError.(type) {
	case error:
		std.Fatalf(errAndStack(v))
	case string:
		std.Fatalf(v, args...)
	}
}

func Panic(formatOrError interface{}, args ...interface{}) {
	args = std.parseArgs(args...)

	switch v := formatOrError.(type) {
	case error:
		std.Panicf(errAndStack(v))
	case string:
		std.Panicf(v, args...)
	}
}

func Dump(formatOrObject interface{}, args ...interface{}) {
	args = std.parseArgs(args...)

	if !std.Verbose() {
		return
	}

	switch v := formatOrObject.(type) {
	case string:
		args, obj := std.dumpObject(args)
		msg := fmt.Sprintf(v, args...)
		dump := spew.Sdump(obj)
		std.Debugf("%s\n%s", msg, dump)
	default:
		dump := spew.Sdump(v)
		std.Debugf("\n%s", dump)
	}
}

func JSON(formatOrObject interface{}, args ...interface{}) {
	args = std.parseArgs(args...)

	if !std.Verbose() {
		return
	}

	switch v := formatOrObject.(type) {
	case string:
		args, obj := std.dumpObject(args)
		msg := fmt.Sprintf(v, args...)
		std.Debugf("%s\n%s", msg, json.Encode(obj))
	default:
		std.Debugf("\n%s", json.Encode(v))
	}
}

func Request(req *http.Request, args ...interface{}) error {
	args = std.parseArgs(args...)

	if !std.Verbose() {
		return nil
	}

	dump, err := httputil.DumpRequest(req, true)
	if err != nil {
		std.Errorf("Failed to dump request: %v", err)
		return fmt.Errorf("Failed to dump request: %v", err)
	}
	std.Debug(string(dump))
	return nil
}

func RequestOut(req *http.Request, args ...interface{}) error {
	args = std.parseArgs(args...)

	if !std.Verbose() {
		return nil
	}

	dump, err := httputil.DumpRequestOut(req, true)
	if err != nil {
		std.Errorf("Failed to dump request: %v", err)
		return fmt.Errorf("Failed to dump request: %v", err)
	}
	std.Debug(string(dump))
	return nil
}

func Response(res *http.Response, args ...interface{}) error {
	args = std.parseArgs(args...)

	if !std.Verbose() {
		return nil
	}

	dump, err := httputil.DumpResponse(res, true)
	if err != nil {
		std.Errorf("Failed to dump request: %v", err)
		return fmt.Errorf("Failed to dump request: %v", err)
	}
	std.Debug(string(dump))
	return nil
}

func Stack(args ...interface{}) {
	args = std.parseArgs(args...)

	if len(args) == 0 {
		std.Debugf(stack(4))
		return
	}

	formatOrError := args[0]

	switch v := formatOrError.(type) {
	case error:
		std.Debugf(errAndStack(v))
	case string:
		msg := fmt.Sprintf(v, args[1:]...)
		std.Debugf(msg + stack(4))
	}
}
