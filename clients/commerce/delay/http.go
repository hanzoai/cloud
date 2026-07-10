package delay

import (
	"context"
	"encoding/gob"
	"net/http"
	"reflect"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

// RequestHeaders contains metadata from the task queue request.
// This replaces taskqueue.RequestHeaders from appengine.
type RequestHeaders struct {
	TaskName       string
	TaskRetryCount int64
	QueueName      string
}

// ParseRequestHeaders extracts task queue headers from an HTTP request.
// This is a compatibility function for HTTP-based task invocation.
func ParseRequestHeaders(h http.Header) *RequestHeaders {
	return &RequestHeaders{
		TaskName:       h.Get("X-Task-Name"),
		TaskRetryCount: parseRetryCount(h.Get("X-Task-Retry-Count")),
		QueueName:      h.Get("X-Queue-Name"),
	}
}

func parseRetryCount(s string) int64 {
	if s == "" {
		return 0
	}
	var count int64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			count = count*10 + int64(c-'0')
		} else {
			break
		}
	}
	return count
}

// RunFunc handles HTTP requests to execute delayed functions.
// This is maintained for backward compatibility with HTTP-based task queues.
// In the new implementation, tasks are executed directly via goroutines,
// but this handler can still be used if you want to dispatch tasks via HTTP.
func RunFunc(c context.Context, w http.ResponseWriter, req *http.Request) {
	defer req.Body.Close()

	// Parse headers and add to context
	headers := ParseRequestHeaders(req.Header)
	c = context.WithValue(c, headersContextKey, headers)

	var inv invocation
	if err := gob.NewDecoder(req.Body).Decode(&inv); err != nil {
		log.Error(c, "delay: failed decoding task payload: %v", err)
		log.Warn(c, "delay: dropping task")
		return
	}

	funcsMu.RLock()
	f := Funcs[inv.Key]
	funcsMu.RUnlock()

	if f == nil {
		log.Error(c, "delay: no func with key %q found", inv.Key)
		log.Warn(c, "delay: dropping task")
		return
	}

	ft := f.fv.Type()
	in := []reflect.Value{reflect.ValueOf(c)}
	for _, arg := range inv.Args {
		var v reflect.Value
		if arg != nil {
			v = reflect.ValueOf(arg)
		} else {
			// Task was passed a nil argument, so we must construct
			// the zero value for the argument here.
			n := len(in) // we're constructing the nth argument
			var at reflect.Type
			if !ft.IsVariadic() || n < ft.NumIn()-1 {
				at = ft.In(n)
			} else {
				at = ft.In(ft.NumIn() - 1).Elem()
			}
			v = reflect.Zero(at)
		}
		in = append(in, v)
	}
	out := f.fv.Call(in)

	if n := ft.NumOut(); n > 0 && ft.Out(n-1) == errorType {
		if errv := out[n-1]; !errv.IsNil() {
			log.Error(c, "delay: func failed (will retry): %v", errv.Interface())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
}

// GetRequestHeaders retrieves the task queue headers from the context.
// Returns nil if called outside of a delay function execution.
func GetRequestHeaders(ctx context.Context) *RequestHeaders {
	h, _ := ctx.Value(headersContextKey).(*RequestHeaders)
	return h
}
