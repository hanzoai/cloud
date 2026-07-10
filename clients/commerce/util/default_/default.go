package default_

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	// "github.com/hanzoai/cloud/clients/commerce/util/exec"
	hashid "github.com/hanzoai/cloud/clients/commerce/util/hashid/http"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
	"github.com/hanzoai/cloud/clients/commerce/util/task"
	"github.com/hanzoai/cloud/clients/commerce/util/template"

	// Imported for side-effect, ensures tasks are registered
	_ "github.com/hanzoai/cloud/clients/commerce/api/checkout/tasks"
	_ "github.com/hanzoai/cloud/clients/commerce/cron/tasks"
	_ "github.com/hanzoai/cloud/clients/commerce/email/tasks"
	_ "github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	_ "github.com/hanzoai/cloud/clients/commerce/models/fixtures/users"
	_ "github.com/hanzoai/cloud/clients/commerce/models/migrations"
	_ "github.com/hanzoai/cloud/clients/commerce/models/referrer/tasks"
	_ "github.com/hanzoai/cloud/clients/commerce/models/webhook/tasks"
	_ "github.com/hanzoai/cloud/clients/commerce/util/aggregate/tasks"
	// _ "github.com/hanzoai/cloud/clients/commerce/thirdparty/salesforce/tasks"
)

func Init() {
	gin.SetMode(gin.ReleaseMode)

	router := router.New("default")

	// Index, development has nice index with links
	if config.IsDevelopment {
		router.GET("/", func(c *gin.Context) {
			template.Render(c, "index.html")
		})
	} else {
		router.GET("/", func(c *gin.Context) {
			c.String(200, "ok")
		})
	}

	// Monitoring test
	router.GET("/wake-up", func(c *gin.Context) {
		log.Panic("I think I heard, I think I heard a shot.")
	})

	// Setup routes for delay funcs
	router.POST(delay.Path, func(c *gin.Context) {
		ctx := middleware.GetContext(c)
		delay.RunFunc(ctx, c.Writer, c.Request)
	})

	// Setup routes for tasks
	task.SetupRoutes(router)

	// Setup hashid routes
	hashid.SetupRoutes(router)

	// Development-only routes below
	if config.IsProduction {
		return
	}

	// Static assets
	router.GET("/static/*file", middleware.Static("static/"))
	router.GET("/assets/*file", middleware.Static("assets/"))
}
