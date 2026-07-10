package task

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
	"github.com/hanzoai/cloud/clients/commerce/util/template"
)

// Setup handlers for HTTP registered tasks
func SetupRoutes(router router.Router) {
	// Redirects
	router.GET("/task", func(c *gin.Context) {
		c.Redirect(301, "/tasks")
	})

	router.GET("/tasks", func(c *gin.Context) {
		template.Render(c, "tasks.html", "tasks", Names())
	})

	// Show task
	router.GET("/task/:name", func(c *gin.Context) {
		name := c.Params.ByName("name")
		template.Render(c, "task.html", "task", name)
	})

	// Run task
	router.POST("/task/:name", func(c *gin.Context) {
		name := c.Params.ByName("name")
		Run(c, name)
		template.Render(c, "task-running.html", "task", name)
	})

	router.GET("/run-tasks", func(c *gin.Context) {
		http.Render(c, 200, Names())
	})

	router.GET("/run-task/:name", func(c *gin.Context) {
		name := c.Params.ByName("name")
		Run(c, name)
		c.Redirect(301, "/run-task/"+name+"/started")
	})

	router.GET("/run-task/:name/started", func(c *gin.Context) {
		name := c.Params.ByName("name")
		http.Render(c, 200, struct {
			Msg string `json:"msg"`
		}{name + "-started"})
	})
}
