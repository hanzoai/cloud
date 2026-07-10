package notification

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	notificationModel "github.com/hanzoai/cloud/clients/commerce/models/notification"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	api := rest.New(notificationModel.Notification{})
	api.POST("/:notificationid/resend", namespaced, Resend)
	api.Route(router, args...)
}

// Resend resets a notification's status to pending so it can be re-delivered.
func Resend(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	id := c.Params.ByName("notificationid")

	n := notificationModel.New(db)
	if err := n.GetById(id); err != nil {
		http.Fail(c, 404, "No notification found with id: "+id, err)
		return
	}

	// Reset status to pending for re-delivery
	n.Status = notificationModel.Pending
	n.ExternalId = ""

	if err := n.Update(); err != nil {
		http.Fail(c, 500, "Failed to resend notification", err)
		return
	}

	http.Render(c, 200, n)
}
