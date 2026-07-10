package search

import (
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/datastore/key"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/hashid"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	searchutil "github.com/hanzoai/cloud/clients/commerce/util/search"
)

func searchUser(c *gin.Context) {
	q := c.Request.URL.Query().Get("q")

	u := user.User{}
	index, err := searchutil.Open(mixin.DefaultIndex)
	if err != nil {
		http.Fail(c, 404, fmt.Sprintf("Failed to find index 'user'"), err)
		return
	}

	db := datastore.New(middleware.GetNamespace(c))
	keys := make([]iface.Key, 0)
	for t := index.Search(db.Context, q, &searchutil.SearchOptions{
		Refinements: []searchutil.Facet{
			{
				Name:  "kind",
				Value: u.Kind(),
			},
		},
	}); ; {
		var doc user.Document
		_, err := t.Next(&doc) // We use the int id stored on the doc rather than the key
		if err == searchutil.Done {
			break
		}
		if err != nil {
			http.Fail(c, 404, fmt.Sprintf("Failed to search index 'user' %v", err), err)
			return
		}

		keys = append(keys, key.FromDBKey(hashid.MustDecodeKey(db.Context, doc.Id())))
	}

	users := make([]user.User, len(keys))
	if err := db.GetMulti(keys, users); err != nil {
		// http.Fail(c, 500, fmt.Sprintf("Failed to get users %v", err), err)
		// return
	}

	http.Render(c, 200, users)
}
