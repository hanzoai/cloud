package api

import (
	commerceapi "github.com/hanzoai/cloud/clients/commerce/api/api"
)

func init() {
	commerceapi.RegisterRoute(Route)
}
