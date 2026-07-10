package router

import (
	"github.com/gin-gonic/gin"
)

type Router interface {
	Any(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	DELETE(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	GET(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	HEAD(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	OPTIONS(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	PATCH(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	POST(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	PUT(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	Group(relativePath string, handlers ...gin.HandlerFunc) *gin.RouterGroup
	Handle(httpMethod, relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes
	Static(relativePath, root string) gin.IRoutes
	Use(middlewares ...gin.HandlerFunc) gin.IRoutes
}
