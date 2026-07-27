package core

import (
	"encoding/json"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// The /v1 envelope writers { status, msg, data, data2 } the operator's transport
// decodes (get<T> reads data; getList<T> reads data + data2 total).
//
// These are thin call-throughs to the ONE implementation in package cloud
// (cloud.OK/OKList/OKRaw/Fail). admin keeps its `core.OK` spelling — the admin
// handlers that call these are unchanged — but there is a single envelope writer
// for the whole binary, so a subsystem outside admin uses cloud.OK directly
// without importing this admin-scoped package (and its iam fan-in).

// OK writes a { status:"ok", data } envelope (the get<T> shape).
func OK(c *zip.Ctx, data any) error { return cloud.OK(c, data) }

// OKList writes a { status:"ok", data:[...], data2:total } envelope (getList<T>).
func OKList(c *zip.Ctx, rows any, total int) error { return cloud.OKList(c, rows, total) }

// OKRaw writes a { status:"ok", data:<raw>, data2:total } envelope, forwarding an
// IAM payload verbatim so its exact wire shape (Role, Application, Record, User)
// reaches the operator field-for-field.
func OKRaw(c *zip.Ctx, rows json.RawMessage, total int) error { return cloud.OKRaw(c, rows, total) }

// Fail writes a { status:"error", msg } envelope. The operator's transport maps a
// non-ok envelope to a surfaced error (never a fabricated value).
func Fail(c *zip.Ctx, msg string) error { return cloud.Fail(c, msg) }
