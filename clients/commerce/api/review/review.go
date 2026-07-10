package review

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/review"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/recaptcha"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/router"
)

func forced404(c *gin.Context) {
	http.Fail(c, 404, "Review does not exist", nil)
}

func get(r *rest.Rest) func(c *gin.Context) {
	return func(c *gin.Context) {
		if !r.CheckPermissions(c, "get") {
			return
		}

		id := c.Params.ByName(r.ParamId)

		org := middleware.GetOrganization(c)
		db := datastore.New(org.Namespaced(c))

		rev := review.New(db)
		if err := rev.GetById(id); err != nil {
			http.Fail(c, 400, "Failed to query review", err)
			return
		}

		if !rev.Enabled {
			http.Fail(c, 404, "Review does not exist", nil)
			return
		}

		http.Render(c, 200, rev)
	}
}

func list(r *rest.Rest) func(c *gin.Context) {
	return func(c *gin.Context) {
		if !r.CheckPermissions(c, "list") {
			return
		}

		query := c.Request.URL.Query()

		// Determine deafult sort order
		sortField := query.Get("sort")
		if sortField == "" {
			sortField = r.DefaultSortField
		}

		org := middleware.GetOrganization(c)
		db := datastore.New(org.Namespaced(c))

		// Create query
		q := review.Query(db).Order(sortField).Filter("Enabled=", true)

		// Update query with page/display params
		var display int
		var err error
		pageStr := query.Get("page")
		displayStr := query.Get("display")
		limitStr := query.Get("limit")

		// if we have pagination values, then trigger pagination calculations
		if displayStr != "" {
			if display, err = strconv.Atoi(displayStr); err == nil && display > 0 {
				q = q.Limit(display)
			} else {
				r.Fail(c, 500, "'display' must be positive and non-zero.", err)
				return
			}
		}

		if pageStr != "" && displayStr != "" {
			if page, err := strconv.Atoi(pageStr); err == nil && page > 0 {
				q = q.Offset(display * (page - 1))
			} else {
				r.Fail(c, 500, "'page' must be positive and non-zero.", err)
				return
			}
		}

		var revs []review.Review
		if _, err = q.GetAll(&revs); err != nil {
			r.Fail(c, 500, "Failed to list "+r.Kind, err)
			return
		}

		count, err := q.Count()
		if err != nil {
			r.Fail(c, 500, "Could not count the models.", err)
			return
		}

		if limitStr != "" {
			if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
				count = limit
			}
		}

		r.Render(c, 200, rest.Pagination{
			Page:    pageStr,
			Display: displayStr,
			Models:  revs,
			Count:   count,
		})
	}
}

type createReq struct {
	*review.Review
	Captcha string `json:"g-recaptcha-response"`
}

func post(r *rest.Rest) func(c *gin.Context) {
	return func(c *gin.Context) {
		if !r.CheckPermissions(c, "create") {
			return
		}

		org := middleware.GetOrganization(c)
		db := datastore.New(org.Namespaced(c))

		req := &createReq{}
		req.Review = review.New(db)

		rev := req.Review

		if err := json.Decode(c.Request.Body, &req); err != nil {
			r.Fail(c, 400, "Failed decode request body", err)
			return
		}

		if org.Recaptcha.Enabled && !recaptcha.Challenge(db.Context, org.Recaptcha.SecretKey, req.Captcha) {
			http.Fail(c, 400, "Captcha needs to be completed", errors.New("Captcha needs to be completed"))
			return
		}

		if err := rev.Create(); err != nil {
			r.Fail(c, 500, "Failed to create "+r.Kind, err)
		} else {
			c.Writer.Header().Add("Location", c.Request.URL.Path+"/"+rev.Id())
			r.Render(c, 201, rev)
		}
	}
}

func Route(router router.Router, args ...gin.HandlerFunc) {
	api := rest.New(review.Review{})

	api.Update = forced404
	api.Patch = forced404
	api.Get = get(api)
	api.List = list(api)
	api.Create = post(api)

	api.Route(router, args...)
}
