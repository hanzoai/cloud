package webhook

import (
	"context"
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/webhook/tasks"
)

func Emit(ctx interface{}, org string, event string, data interface{}) {
	var reqCtx context.Context

	switch v := ctx.(type) {
	case *gin.Context:
		if c, exists := v.Get("context"); exists {
			reqCtx = c.(context.Context)
		} else {
			reqCtx = v.Request.Context()
		}
	case context.Context:
		reqCtx = v
	}

	// If we have a model, fire off a json-safe copy of it
	model, ok := data.(mixin.Entity)
	if ok {
		tasks.Emit.Call(reqCtx, org, event, model.CloneFromJSON())
	} else {
		tasks.Emit.Call(reqCtx, org, event, data)
	}
}
