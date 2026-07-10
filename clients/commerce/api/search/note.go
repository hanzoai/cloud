package search

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/note"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type searchReq struct {
	After  time.Time `json:"after"`
	Before time.Time `json:"before"`
}

func searchNote(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	req := &searchReq{}
	if err := json.Decode(c.Request.Body, req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	nts := make([]*note.Note, 0)

	q := note.Query(db).Filter("Enabled=", true).Filter("Time>", req.After).Filter("Time<=", req.Before)
	if _, err := q.GetAll(&nts); err != nil {
		http.Fail(c, 500, "Failed to get logs", err)
		return
	}

	http.Render(c, 200, nts)
}
