package query

import (
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/datastore/key"
	"github.com/hanzoai/cloud/clients/commerce/util/hashid"
	"github.com/hanzoai/cloud/clients/commerce/util/reflect"
)

var newKeyFromInt = key.NewFromInt

// Get coupon from id
func (q *Query) couponFromId(id string, dst interface{}) (iface.Key, bool, error) {
	code := strings.ToUpper(id)

	// The Coupon model stores Code in field Code_ (serialized as "code_" by marshalForDB).
	k, ok, err := q.Filter("Code_=", code).First(dst)
	if ok {
		return k, true, nil
	}
	if err != nil {
		return nil, false, err
	}

	// Get ids from coupon id
	ids, err := hashid.Decode(id)
	if err != nil {
		return nil, false, InvalidKey
	}

	if len(ids) != 3 || ids[0] != 3333 {
		return nil, false, InvalidKey
	}

	// Recreate coupon key
	dsKey, err := newKeyFromInt(q.ctx, "coupon", ids[1], nil)
	if err != nil {
		return nil, false, err
	}

	// Fetch coupon using key
	_, ok, err = q.ByKey(dsKey, dst)
	if err != nil {
		return nil, false, err
	}

	if !ok {
		return nil, false, nil
	}

	// Set RawCode on fetched entity in case this was not parsed from JSON
	reflect.SetField(reflect.ValueOf(dst), "RawCode", id)

	return dsKey, true, nil
}

// Get order from id
func (q *Query) orderFromId(id string, dst interface{}) (iface.Key, bool, error) {
	// Coerce into number type
	n, err := strconv.Atoi(id)
	if err != nil {
		return nil, false, err
	}

	k, ok, err := q.Filter("Number=", n).First(dst)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}

	return k, ok, err
}
