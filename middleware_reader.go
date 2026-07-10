package cloud

import (
	"net/http"

	"github.com/hanzoai/cloud/role"
	"github.com/zap-proto/zip"
)

// ReaderGuard fails CLOSED on a read replica: every mutating request is rejected
// at the app boundary, so correctness never rides on gateway routing.
//
// A Reader opens the KMS store read-only and hydrates audit / durable tasks /
// per-org SQLite from the replication stream into an EPHEMERAL dir. A write
// that reached a reader — a mis-route, a rollout race — would persist to that
// ephemeral dir, answer 2xx, then VANISH on the next restart: silent data loss,
// and UNAUDITED (the audit chain is writer-only). ONE guard gates ALL stores at
// once, not just KMS: GET/HEAD/OPTIONS are served; every other verb is 405 with
// a pointer to retry against the writer. Mounted only when role.IsReader(), so a
// Writer (the default, unset CLOUD_ROLE) is untouched.
func ReaderGuard() zip.Handler {
	return func(c *zip.Ctx) error {
		switch c.Method() {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return c.Next()
		}
		return c.JSON(http.StatusMethodNotAllowed, map[string]string{
			"error": "read-only replica: retry this write against the writer",
			"role":  string(role.Reader),
		})
	}
}
