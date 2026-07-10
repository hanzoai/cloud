package http

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

func Render(c *gin.Context, status int, src interface{}) {
	// Write headers
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(status)

	// Render response
	c.Writer.Write(json.EncodeBytes(src))
}

func Fail(c *gin.Context, status int, message interface{}, err error) {
	// Default response
	res := Error{"api-error", "", "", ""}

	// Parse message type
	switch v := message.(type) {
	case Error:
		res = v
	case string:
		res.Message = v
	case map[string]string:
		if typ, ok := v["type"]; ok {
			res.Type = typ
		}
		if code, ok := v["code"]; ok {
			res.Code = code
		}
		if msg, ok := v["message"]; ok {
			res.Message = msg
		}
		if param, ok := v["param"]; ok {
			res.Param = param
		}
	}

	// Force 402 on auth errors
	if res.Type == "authorization-error" {
		status = 402
	}

	// Write headers
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(status)

	// Render JSON error message
	c.Writer.Write(json.EncodeBytes(gin.H{"error": res}))

	// Log error
	if err != nil {
		if status < 500 {
			log.Warn(err, c)
		} else {
			log.Error(err, c)
		}
	}

	// Stop processing middleware
	c.AbortWithStatus(status)
}
