package core

import (
	"encoding/json"

	"github.com/zap-proto/zip"
)

// The /v1 envelope writers { status, msg, data, data2 } the operator's transport
// decodes (get<T> reads data; getList<T> reads data + data2 total).

// OK writes a { status:"ok", data } envelope (the get<T> shape).
func OK(c *zip.Ctx, data any) error {
	return c.JSON(200, map[string]any{"status": "ok", "msg": "", "data": data})
}

// OKList writes a { status:"ok", data:[...], data2:total } envelope (getList<T>).
func OKList(c *zip.Ctx, rows any, total int) error {
	return c.JSON(200, map[string]any{"status": "ok", "msg": "", "data": rows, "data2": total})
}

// OKRaw writes a { status:"ok", data:<raw>, data2:total } envelope, forwarding an
// IAM payload verbatim so its exact wire shape (Role, Application, Record, User)
// reaches the operator field-for-field.
func OKRaw(c *zip.Ctx, rows json.RawMessage, total int) error {
	if len(rows) == 0 {
		rows = json.RawMessage("[]")
	}
	return c.JSON(200, map[string]any{"status": "ok", "msg": "", "data": rows, "data2": total})
}

// Fail writes a { status:"error", msg } envelope. The operator's transport maps a
// non-ok envelope to a surfaced error (never a fabricated value).
func Fail(c *zip.Ctx, msg string) error {
	return c.JSON(200, map[string]any{"status": "error", "msg": msg, "data": nil})
}
