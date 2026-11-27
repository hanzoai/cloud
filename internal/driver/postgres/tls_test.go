package postgres

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// requireTLS decides from the configuration pgx will dial with, not from the text of
// the URL: a DSN that says sslmode twice is read by pgx by its last word.
func TestRequireTLSReadsWhatPgxWillDo(t *testing.T) {
	for dsn, refused := range map[string]bool{
		"postgres://u@db.example.com/x":                                          true, // prefer, the default
		"postgres://u@db.example.com/x?sslmode=prefer":                           true,
		"postgres://u@db.example.com/x?sslmode=allow":                            true,
		"postgres://u@db.example.com/x?sslmode=disable":                          true,
		"postgres://u@db.example.com/x?sslmode=require&sslmode=disable":          true, // pgx takes the last
		"postgres://u@db.example.com/x?sslmode=verify-full&sslmode=disable":      true,
		"postgres://u@db.example.com/x?sslmode=disable&sslmode=require":          false, // and so does this
		"postgres://u@db.example.com/x?sslmode=require":                          false,
		"postgres://u@db.example.com/x?sslmode=verify-ca":                        false,
		"postgres://u@db.example.com/x?sslmode=verify-full":                      false,
		"postgres://u@db.example.com,other.example.com/x?sslmode=require":        false,
		"postgres://u@db.example.com,other.example.com/x?sslmode=prefer":         true,
		"postgres://u@db.example.com,localhost/x?sslmode=disable":                true, // one of the hosts is not here
		"postgres://u@127.0.0.1:5432/x?sslmode=disable":                          false,
		"postgres://u@localhost/x?sslmode=disable":                               false,
		"postgres://u@[::1]/x?sslmode=disable":                                   false,
		"postgres://u@/x?host=/var/run/postgresql&sslmode=disable":               false,
		"postgres://u@db.example.com/x?host=/var/run/postgresql&sslmode=disable": false, // host= wins: a socket
	} {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("%s: %v", dsn, err)
		}
		err = requireTLS(cfg)
		if refused && (err == nil || !strings.Contains(err.Error(), "sslmode=require")) {
			t.Errorf("%s: %v; want a refusal naming sslmode=require", dsn, err)
		}
		if !refused && err != nil {
			t.Errorf("%s: refused: %v", dsn, err)
		}
	}
}
