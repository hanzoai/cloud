package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/user"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var userIds = map[string]bool{
	"O0HkQ5EyWsK2EAr": true,
	"x3HD9RX0GTnQm79": true,
	"49HQ7nopRsyW1z3": true,
	"dpHdZbwbhwl4be":  true,
	"x3HDN6RkWfnQm79": true,
	"YYHl1eqRGT9vnoD": true,
	"PWH8YN79RFd705A": true,
	"g6HlpEqYeCO7N5x": true,
	"NeH11eqb6uv01Zb": true,
	"BQHgrDxGF5bvQy":  true,
	"49HQ2j0B2uyW1z3": true,
	"zJH2or8wtjBQ98":  true,
	"AyHXO0KEYfnWbwo": true,
	"3PHOPvrWSo1P46":  true,
	"Q0HZZy36vcxmlvq": true,
	"nZHXZn74BT7rWx2": true,
	"g6HZ4bKnTO7N5x":  true,

	"NeH1Ovm6GIv01Zb": true,
	"q2H939XE8f8EODZ": true,
	"KyHj71QNbUAlB3R": true,
	"GvHjmGWREfgkW40": true,
	"evHG3ggKSJwBNP":  true,
	"BQHbbRYdbT5bvQy": true,
	"evH2n3DK5iJwBNP": true,
	"BQHbkynzlC5bvQy": true,
	"NeHk1D4ESv01Zb":  true,
	"5xH2qY5pI8zEnG":  true,
	"bNHEjrZAuxZ4jP":  true,
	"Q0HdoygKuxmlvq":  true,

	"DqHvDpQX2Sko1Gq": true,
	"81Hw67bot30Dod":  true,
	"w4HrZnrAI3jNdr":  true,
	"81HgE4KZ7H30Dod": true,
	"JyH54ZZKH0RJvg":  true,
	"RyHl3KxvSyWgv9":  true,
	"RyHA9o27uyWgv9":  true,
}

var _ = New("cleanup-damon",
	func(c *gin.Context) []interface{} {
		db := ds.New(c)

		c.Set("namespace", "damon")
		db.SetNamespace("damon")

		return NoArgs
	},
	func(db *ds.Datastore, u *user.User) {
		if userIds[u.Id()] {
			u.DeleteDocument()
		}
	},
	func(db *ds.Datastore, o *order.Order) {
		if userIds[o.UserId] {
			o.DeleteDocument()
		}
	},
)
