// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// envelope.go — the ONE /v1 response envelope, at the ONE home every subsystem
// already imports.
//
// The canonical /v1 envelope is { status, msg, data, data2 }: the operator's
// transport decodes get<T> from `data` and getList<T> from `data` + `data2`
// (the total). This shape was first written in clients/admin/core, but the
// writers are pure over *zip.Ctx and every subsystem needs them — trapping them
// under clients/admin (whose sibling files pull in cloud + admin/iam +
// principal) would force any package that wants ONE envelope to drag the whole
// admin IAM fan-in in transitively. So the writers live HERE, in package cloud,
// beside Handle/Mount/Terminal — the handler ergonomics every subsystem already
// reaches for. clients/admin/core.OK/OKList/OKRaw/Fail now delegate here, so
// there is ONE implementation and admin keeps its spelling.
//
// A subsystem writes a success body with cloud.OK / cloud.OKList / cloud.OKRaw
// and a surfaced error with cloud.Fail — never a hand-rolled
// map[string]any{"status":...}. Handlers with a typed public body that is NOT
// the operator envelope (a health probe, an OAuth token, an SDK-shaped struct)
// keep returning c.JSON with their own type — this envelope is for the operator
// get<T>/getList<T> contract, not a mandate on every response.

package cloud

import (
	"encoding/json"

	"github.com/zap-proto/zip"
)

// OK writes a { status:"ok", data } envelope (the get<T> shape).
func OK(c *zip.Ctx, data any) error {
	return c.JSON(200, map[string]any{"status": "ok", "msg": "", "data": data})
}

// OKList writes a { status:"ok", data:[...], data2:total } envelope (getList<T>).
func OKList(c *zip.Ctx, rows any, total int) error {
	return c.JSON(200, map[string]any{"status": "ok", "msg": "", "data": rows, "data2": total})
}

// OKRaw writes a { status:"ok", data:<raw>, data2:total } envelope, forwarding a
// pre-encoded payload verbatim so its exact wire shape reaches the operator
// field-for-field. An empty payload is normalized to an empty array.
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
