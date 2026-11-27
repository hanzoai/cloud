package postgres

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/orm/tenant"

	// The one PostgreSQL driver, registered as "pgx" — the one orm/tenant speaks.
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/locus"
)

// AdminEnv names the hanzo-sql administrator's DSN.
const AdminEnv = locus.AdminEnv

// Tenants opens a shared store as a PostgreSQL database named for the app, reached
// as a login named for the app that holds no privilege of its own; tenant.Open
// refuses any other arrangement. The login's password is derived, not stored: HKDF
// over the deployment's data-plane master, which every process already holds, so
// every replica derives the same one and rotating the master rotates it. The
// administrator opens the door once at boot — provisioning the login and the
// database, idempotently — and no statement the app serves runs as it.
func (engine) Tenants(ctx context.Context, t locus.Tenancy) (*tenant.DB, func() error, bool, error) {
	admin := strings.TrimSpace(env.Or(AdminEnv, ""))
	if admin == "" {
		if DSN() != "" {
			return nil, nil, false, fmt.Errorf("%s: %s is set, so this deployment keeps its stores on hanzo-sql, and %s is not: "+
				"a shared store cannot provision its login without it, and would be a SQLite file private to this pod; set %s",
				t.Name, Env, AdminEnv, AdminEnv)
		}
		return nil, nil, false, nil
	}
	dsn, err := provision(ctx, admin, t.Name)
	if err != nil {
		return nil, nil, false, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, false, fmt.Errorf("%s: open the store: %w", t.Name, err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	st, err := tenant.Open(ctx, tenant.Config{Driver: "pgx", DB: db, Schema: t.Name, Tables: t.Tables, Tenant: t.Tenant, Audit: t.Audit})
	if err != nil {
		_ = db.Close()
		return nil, nil, false, err
	}
	return st, db.Close, true, nil
}

// adminURL reads the administrator's DSN as the URL pgx dials. The estate keeps
// one PostgreSQL credential per cluster and spells its scheme for whichever
// client reads it — postgres://, postgresql://, or a SQLAlchemy dialect such as
// postgresql+asyncpg:// — so every spelling of that one URL is read, and
// anything else is refused rather than guessed at.
func adminURL(admin string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(admin))
	if err != nil || u.Host == "" || u.User == nil {
		return nil, false
	}
	scheme, _, _ := strings.Cut(strings.ToLower(u.Scheme), "+")
	if scheme != "postgres" && scheme != "postgresql" {
		return nil, false
	}
	u.Scheme = "postgres"
	return u, true
}

// provision makes the app's login and database on hanzo-sql, as the
// administrator, and answers the DSN the app connects with.
func provision(ctx context.Context, admin, name string) (string, error) {
	u, ok := adminURL(admin)
	if !ok {
		return "", fmt.Errorf("%s: %s is not a postgres URL", name, AdminEnv)
	}
	admin = u.String()
	secret, err := password(name)
	if err != nil {
		return "", err
	}
	super, err := sql.Open("pgx", admin)
	if err != nil {
		return "", fmt.Errorf("%s: reach hanzo-sql: %w", name, err)
	}
	defer func() { _ = super.Close() }()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := tenant.Provision(ctx, super, name, name); err != nil {
		return "", fmt.Errorf("%s: provision: %w", name, err)
	}
	// The password is base64url — [A-Za-z0-9_-] — so it is a safe literal, and
	// the name was held to an identifier above; ALTER ROLE takes no parameter.
	if _, err := super.ExecContext(ctx, `ALTER ROLE "`+name+`" PASSWORD '`+secret+`'`); err != nil {
		return "", fmt.Errorf("%s: set the login's password: %w", name, err)
	}
	u.User = url.UserPassword(name, secret)
	u.Path = "/" + name
	return u.String(), nil
}

// password derives the app's login password from the data-plane master.
func password(name string) (string, error) {
	master := locus.Master()
	if len(master) == 0 {
		return "", errors.New(name + ": no data-plane master to derive the store's credential from")
	}
	key, err := hkdf.Key(sha256.New, master, nil, "hanzo/"+name+"/sql/login", 32)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(key), nil
}
