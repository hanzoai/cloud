package studio

// page.go — lists are newest first and paged by a cursor that names the last row
// a page held; a miss of any kind is the one 404.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hanzoai/orm/tenant"
	"github.com/zap-proto/zip"
)

// notFound turns a store miss into the one 404.
func notFound(err error) error {
	if errors.Is(err, errGone) {
		return zip.ErrNotFound(strings.TrimSuffix(err.Error(), " "+errGone.Error()) + " not found")
	}
	return err
}

// mark is a position in a newest-first list.
type mark struct {
	at  int64
	id  string
	col string
}

// before is every row after this mark in a newest-first list.
func (m *mark) before() tenant.Cond {
	col := m.col
	if col == "" {
		col = "created"
	}
	return tenant.Or(tenant.Lt(col, m.at), tenant.And(tenant.Eq(col, m.at), tenant.Lt("id", m.id)))
}

// paging reads a list's limit and cursor.
func paging(limit int, cursor string) (int, *mark, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if cursor == "" {
		return limit, nil, nil
	}
	raw, err := hex.DecodeString(cursor)
	if err != nil {
		return 0, nil, zip.ErrBadRequest("cursor is not one this list gave")
	}
	var c struct {
		At int64  `json:"a"`
		ID string `json:"i"`
	}
	if err := json.Unmarshal(raw, &c); err != nil || c.ID == "" {
		return 0, nil, zip.ErrBadRequest("cursor is not one this list gave")
	}
	return limit, &mark{at: c.At, id: c.ID}, nil
}

// cursorOf is the cursor that continues after (at, id).
func cursorOf(at int64, id string) string {
	b, _ := json.Marshal(map[string]any{"a": at, "i": id})
	return hex.EncodeToString(b)
}
