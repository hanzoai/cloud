package task

import (
	"context"
	"reflect"
	"sort"
	"strconv"

	"github.com/hanzoai/cloud/clients/commerce/delay"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/util/fakecontext"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
)

var (
	Registry    = make(map[string][]*Task)
	contextType = reflect.TypeOf((**gin.Context)(nil)).Elem()
)

// A Task which can be invoked later by name or HTTP handler
type Task struct {
	Name     string
	Number   int
	Value    reflect.Value
	Function interface{}
	Delay    *Delay
}

// Details about a Task's delay function, if created with special task.Func helper
type Delay struct {
	Function *delay.Function
	Name     string
}

// Create a new task and register it
func New(name string, fn interface{}) *Task {
	task := new(Task)
	task.Name = name

	// Store function details for later
	task.Function = fn
	task.Value = reflect.ValueOf(fn)

	// Automatically register task and save number for this task name
	task.Number = Register(name, task)

	return task
}

// Register a new task in task registry
func Register(name string, tasks ...*Task) int {
	// Create slice for task set
	_tasks, ok := Registry[name]

	if !ok {
		_tasks = make([]*Task, 0)
	}

	// Append tasks
	Registry[name] = append(_tasks, tasks...)

	return len(Registry[name])
}

// Remove tasks registered under a given name.
func Unregister(name string) {
	delete(Registry, name)
}

// Returns a slice of task names
func Names() []string {
	tasks := make([]string, 0)
	for k, _ := range Registry {
		tasks = append(tasks, k)
	}
	sort.Strings(tasks)
	return tasks
}

// Run task(s) associated with a given name
func Run(ctx *gin.Context, name string, args ...interface{}) {
	tasks, ok := Registry[name]

	if !ok {
		log.Panic("Unknown task: %v", name, ctx)
	}

	for i := 0; i < len(tasks); i++ {
		switch v := tasks[i].Function.(type) {
		case *delay.Function:
			v.Call(middleware.GetContext(ctx), args...)
		case func(context.Context):
			v(middleware.GetContext(ctx)) // TODO: Remove after updating older tasks.
		case func(context.Context, ...interface{}):
			v(middleware.GetContext(ctx), args...)
		case func(*gin.Context):
			v(ctx)
		case func(*gin.Context, ...interface{}):
			v(ctx, args...)
		case *Delay:
			v.Function.Call(middleware.GetContext(ctx), fakecontext.NewContext(ctx))
		default:
			log.Panic("Don't know how to call %v", reflect.ValueOf(v).Type(), ctx)
		}
	}
}

func getGinContext(ctx context.Context, fakectx *fakecontext.Context, ok bool) *gin.Context {
	// If we have a fake context, try to use that
	if ok {
		if c, err := fakectx.Context(ctx); err == nil {
			return c
		}
	}

	return gincontext.New(ctx)
}

// Ensure callbacks passed to `Func` match required signature
func checkFunc(fn interface{}) {
	// Check type of worker func to ensure it matches required signature.
	t := reflect.TypeOf(fn)

	// Ensure that fn is actually a func
	if t.Kind() != reflect.Func {
		log.Panic("Function is required for second parameter")
	}

	// fn should be a function that takes at least three arguments
	argNum := t.NumIn()
	if argNum < 1 {
		log.Panic("Function requires at least one argument")
	}

	// First argument fn should be context.Context
	if t.In(0) != contextType {
		log.Panic("First argument must be *gin.Context: %v", t)
	}
}

// Creates a new delay.Func which will call our fn with context.Context, etc.
func Func(name string, fn interface{}) *delay.Function {
	// Make sure this is a valid func
	checkFunc(fn)

	// Create new task
	task := New(name, fn)
	task.Delay = new(Delay)

	// Increment name for delay.Func if this is a duplicate func
	dname := task.Name
	if task.Number > 1 {
		dname = dname + "-" + strconv.Itoa(task.Number)
	}

	// Create actual delay.Func
	dfunc := delay.Func(dname, func(c context.Context, args ...interface{}) {
		// Try to retrieve fake context from args
		var fakectx *fakecontext.Context
		var ok bool

		// Check if we were passed a fakecontext
		if len(args) > 0 {
			fakectx, ok = args[0].(*fakecontext.Context)
		}

		// Remove fakecontext from args, if it exists
		if ok {
			args = args[:len(args)-1]
		}

		// Recreate gin context from fakecontext if possible, otherwise
		// create a new one using this context.
		ctx := getGinContext(c, fakectx, ok)

		// Build arguments for fn
		in := []reflect.Value{reflect.ValueOf(ctx)}

		// Append variadic args
		for _, arg := range args {
			in = append(in, reflect.ValueOf(arg))
		}

		// Call fn
		task.Value.Call(in)
	})

	// Use delay.Func as task.Function
	task.Function = dfunc

	// Save details of delay func
	task.Delay.Name = dname
	task.Delay.Function = dfunc

	return dfunc
}
