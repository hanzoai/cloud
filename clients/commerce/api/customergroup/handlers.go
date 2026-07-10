package customergroup

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	customergroupModel "github.com/hanzoai/cloud/clients/commerce/models/customergroup"
	"github.com/hanzoai/cloud/clients/commerce/models/customergroupmembership"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func Route(router router.Router, args ...gin.HandlerFunc) {
	namespaced := middleware.Namespace()

	api := rest.New(customergroupModel.CustomerGroup{})
	api.POST("/:customergroupid/members", namespaced, AddMember)
	api.DELETE("/:customergroupid/members/:userId", namespaced, RemoveMember)
	api.GET("/:customergroupid/members", namespaced, ListMembers)
	api.Route(router, args...)

	rest.New(customergroupmembership.CustomerGroupMembership{}).Route(router, args...)
}

// addMemberRequest represents the body for adding a member.
type addMemberRequest struct {
	UserId string `json:"userId"`
}

// AddMember adds a user to a customer group.
func AddMember(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	groupId := c.Params.ByName("customergroupid")

	// Verify group exists
	group := customergroupModel.New(db)
	if err := group.GetById(groupId); err != nil {
		http.Fail(c, 404, "No customer group found with id: "+groupId, err)
		return
	}

	var req addMemberRequest
	if err := json.Decode(c.Request.Body, &req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	if req.UserId == "" {
		http.Fail(c, 400, "userId is required", errors.New("missing userId"))
		return
	}

	// Check for duplicate membership
	existing := customergroupmembership.New(db)
	ok, err := existing.Query().
		Filter("CustomerGroupId=", groupId).
		Filter("UserId=", req.UserId).
		Get()
	if err != nil {
		http.Fail(c, 500, "Failed to query memberships", err)
		return
	}
	if ok {
		http.Fail(c, 409, "User is already a member of this group", errors.New("duplicate membership"))
		return
	}

	// Create membership
	membership := customergroupmembership.New(db)
	membership.CustomerGroupId = groupId
	membership.UserId = req.UserId

	if err := membership.Create(); err != nil {
		http.Fail(c, 500, "Failed to create membership", err)
		return
	}

	http.Render(c, 201, membership)
}

// RemoveMember removes a user from a customer group.
func RemoveMember(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	groupId := c.Params.ByName("customergroupid")
	userId := c.Params.ByName("userId")

	// Find membership
	membership := customergroupmembership.New(db)
	ok, err := membership.Query().
		Filter("CustomerGroupId=", groupId).
		Filter("UserId=", userId).
		Get()
	if err != nil {
		http.Fail(c, 500, "Failed to query memberships", err)
		return
	}
	if !ok {
		http.Fail(c, 404, "Membership not found", errors.New("membership not found"))
		return
	}

	if err := membership.Delete(); err != nil {
		http.Fail(c, 500, "Failed to delete membership", err)
		return
	}

	c.Data(204, "application/json", make([]byte, 0))
}

// ListMembers lists all members of a customer group.
func ListMembers(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	groupId := c.Params.ByName("customergroupid")

	// Verify group exists
	group := customergroupModel.New(db)
	if err := group.GetById(groupId); err != nil {
		http.Fail(c, 404, "No customer group found with id: "+groupId, err)
		return
	}

	var members []*customergroupmembership.CustomerGroupMembership
	q := customergroupmembership.Query(db).Filter("CustomerGroupId=", groupId)
	if _, err := q.GetAll(&members); err != nil {
		http.Fail(c, 500, "Failed to list members", err)
		return
	}

	http.Render(c, 200, members)
}
