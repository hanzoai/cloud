package test

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ginclient"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("middleware/accesstoken", t)
}

const kind = "user"

var (
	ctx ae.Context
	db  *datastore.Datastore
)

// Setup test context
var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	db = datastore.New(ctx)
})

var _ = AfterSuite(func() {
	ctx.Close()
})

type Stub struct {
	Foo string
}

var _ = Describe("middleware/accesstoken", func() {
	Context("accessToken.RequiresOrgToken", func() {
		It("should namespace based on Organization.Name", func() {
			u := user.New(db)
			err := u.Put()
			Expect(err).NotTo(HaveOccurred())

			// create an org
			o := organization.New(db)
			o.Name = "Justin"
			o.SecretKey = []byte("AAA")
			o.Owners = []string{u.Id()}

			// insert into db
			err = o.Put()
			Expect(err).NotTo(HaveOccurred())

			id := o.Name

			// generate accessToken with any permission
			accessToken := o.AddToken("some-token", permission.All)

			// Update organization, so middleware can find it
			err = o.Put()
			Expect(err).NotTo(HaveOccurred())

			// Make request using access token
			client := ginclient.New(ctx)
			// Setup client router to check for token required
			client.Use(middleware.TokenRequired())
			// Return ok if token is valid
			client.Handle("GET", "/", func(c *gin.Context) {
				c.String(200, "ok")
			})
			// Set access token on client
			client.Defaults(func(r *http.Request) {
				r.Header.Set("Authorization", accessToken)
			})
			// Make request
			client.Get("/", nil)

			// middleware generates namespaced context
			org := middleware.GetOrganization(client.Context)

			ctx2 := org.Namespaced(ctx)
			Expect(ctx2).ToNot(Equal(ctx))
			Expect(org).ToNot(Equal(nil))

			// make db from namespaced context
			db2 := datastore.New(ctx2)

			stub2 := Stub{Foo: "1"}
			key2, err := db2.Put("namespaced-things", &stub2)
			Expect(err).ToNot(HaveOccurred())

			// make another namespace context different from returned
			ctx3 := nscontext.WithNamespace(ctx, "empty-namespace")

			// make db from different namespace context
			db3 := datastore.New(ctx3)

			// shouldn't be able to get namespaced key
			stub3 := Stub{}
			key3 := db3.NewKey(key2.Kind(), key2.StringID(), key2.IntID(), nil)
			err = db3.Get(key3, &stub3)
			Expect(err).To(HaveOccurred())
			Expect(stub3.Foo).ToNot(Equal(stub2.Foo))

			// make another namespace context same as returned
			ctx4 := nscontext.WithNamespace(ctx, id)

			// make db from same namespace context
			db4 := datastore.New(ctx4)

			stub4 := Stub{}
			key4 := db4.NewKey(key2.Kind(), key2.StringID(), key2.IntID(), nil)
			err = db4.Get(key4, &stub4)
			Expect(err).ToNot(HaveOccurred())
			Expect(stub4.Foo).To(Equal(stub2.Foo))
		})
	})
})
