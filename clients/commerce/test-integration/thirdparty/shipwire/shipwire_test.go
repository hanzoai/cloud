package test

import (
	"github.com/hanzoai/cloud/clients/commerce/util/fs"
	"github.com/hanzoai/cloud/clients/commerce/util/json"

	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/shipwire/types"
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func decodeRequest(data []byte, dst interface{}) error {
	var req Request
	if err := json.DecodeBytes(data, &req); err != nil {
		return err
	}
	return json.Unmarshal(req.Body.Resource, dst)
}

var _ = Describe("shipwire", func() {
	Context("Date", func() {
		It("Should unmarshall Shipwire dates correctly", func() {
			data := fs.ReadFile("fixtures/tracking.updated.json")
			var t Tracking
			err := decodeRequest(data, &t)
			Expect(err).To(BeNil())
		})
	})
})
