package lineitem

import "github.com/hanzoai/cloud/clients/commerce/util/spew"

type LineItemError struct {
	Item *LineItem
}

func (e LineItemError) Error() string {
	return "Invalid line item, ensure ID, slug or SKU is correct:\n" + spew.Sdump(e.Item)
}
