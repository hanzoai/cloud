// Package main implements commerce-grant — a CLI for manually granting
// subscriptions to users (e.g. gifting a Pro plan to a beta tester).
//
// Usage:
//
//	commerce-grant --email hunter@example.com --plan world-pro
//	commerce-grant --email hunter@example.com --plan world-pro --months 6 --reason "beta gift"
//
// Auth: pulls the IAM admin client-id/secret and the commerce service token
// from environment variables (or, in production, from KMS under project
// hanzo-commerce / path /admin/grant-token). No user-visible UI.
//
// The CLI talks to the same datastore commerce uses at runtime. It calls
// commerce.App.Bootstrap() to wire the DB, IAM middleware, and KMS client,
// then invokes billing/grant.Grant() directly. This keeps the grant path
// tested end-to-end without needing an HTTP server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	commerce "github.com/hanzoai/cloud/clients/commerce"
	"github.com/hanzoai/cloud/clients/commerce/api/billing"
	"github.com/hanzoai/cloud/clients/commerce/auth"
	"github.com/hanzoai/cloud/clients/commerce/billing/grant"
	commerceDatastore "github.com/hanzoai/cloud/clients/commerce/datastore"
	orgModel "github.com/hanzoai/cloud/clients/commerce/models/organization"
)

func main() {
	email := flag.String("email", "", "IAM user email (required)")
	plan := flag.String("plan", "world-pro", "Plan slug (e.g. world-pro)")
	months := flag.Int("months", 12, "Grant duration in months")
	reason := flag.String("reason", "manual gift", "Reason for grant (audit)")
	grantedBy := flag.String("by", defaultGrantedBy(), "Operator identifier (audit)")
	dryRun := flag.Bool("dry-run", false, "Resolve user and plan but do not write")
	flag.Parse()

	if *email == "" {
		die("--email is required")
	}
	if *plan == "" {
		die("--plan is required")
	}

	// Require explicit opt-in to prevent accidental grants in wrong envs.
	if os.Getenv("COMMERCE_GRANT_ALLOW") != "true" && !*dryRun {
		die("set COMMERCE_GRANT_ALLOW=true to confirm this is the right environment (or use --dry-run)")
	}

	// Bootstrap the commerce app: loads config, sets up DB, IAM client, KMS.
	app := commerce.New()
	if err := app.Bootstrap(); err != nil {
		die("commerce bootstrap failed: %v", err)
	}

	// Resolve IAM user → "owner/name".
	iamBase := getEnv("IAM_ISSUER", "https://hanzo.id")
	iamClientID := getEnv("IAM_CLIENT_ID", "")
	iamClientSecret := getEnv("IAM_CLIENT_SECRET", "")
	if iamClientID == "" || iamClientSecret == "" {
		die("IAM_CLIENT_ID and IAM_CLIENT_SECRET required to look up users by email")
	}

	adminIAM := auth.NewIAMAdminClient(iamBase, iamClientID, iamClientSecret,
		&http.Client{Timeout: 15 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	user, err := adminIAM.GetUserByEmail(ctx, *email)
	if err != nil {
		die("IAM lookup failed: %v", err)
	}
	if user == nil {
		die("no IAM user found with email %q", *email)
	}

	subject := user.Subject()
	if subject == "" {
		die("IAM user missing owner/name (raw: %+v)", user)
	}

	// Resolve the plan from the static catalog so we fail fast before touching
	// the datastore.
	plCatalog := billing.LookupStaticPlan(*plan)
	if plCatalog == nil {
		die("plan %q is not in the static catalog (run `commerce seed <org>` first or check plans repo)", *plan)
	}

	fmt.Printf("User:      %s  (email=%s, owner=%s)\n", subject, *email, user.Owner)
	fmt.Printf("Plan:      %s  (%s — $%.2f/mo)\n", plCatalog.Slug, plCatalog.Name, float64(plCatalog.PriceMonth)/100)
	fmt.Printf("Duration:  %d months\n", *months)
	fmt.Printf("Reason:    %s\n", *reason)
	fmt.Printf("GrantedBy: %s\n", *grantedBy)

	if *dryRun {
		fmt.Println("\n[dry-run] no subscription created.")
		return
	}

	// Ensure org exists in commerce (auto-create on first grant — mirrors
	// IAM middleware behaviour for live requests).
	db := commerceDatastore.New(ctx)
	org := orgModel.New(db)
	org.Name = user.Owner
	org.Enabled = true
	if err := org.GetOrCreate("Name=", org.Name); err != nil {
		die("commerce org %s resolve/create failed: %v", org.Name, err)
	}

	// Scope DB to this org namespace for plan/subscription writes.
	nsDs := commerceDatastore.New(ctx)
	nsDs.SetNamespace(org.Namespace())

	catalog := staticCatalogAdapter{}
	result, err := grant.Grant(ctx, nsDs, catalog, grant.Request{
		UserId:    subject,
		PlanSlug:  plCatalog.Slug,
		Duration:  time.Duration(*months) * 30 * 24 * time.Hour,
		Reason:    *reason,
		GrantedBy: *grantedBy,
	})
	if err != nil {
		if errors.Is(err, grant.ErrPlanNotFound) {
			die("plan %q missing from org datastore and catalog", *plan)
		}
		die("grant failed: %v", err)
	}

	fmt.Printf("\nGranted %s to %s (%s), expires %s\n",
		result.PlanSlug, result.UserId, result.SubscriptionID,
		result.PeriodEnd.Format("2006-01-02"))
}

// staticCatalogAdapter bridges billing.LookupStaticPlan → grant.PlanCatalog.
type staticCatalogAdapter struct{}

func (staticCatalogAdapter) Lookup(slug string) *grant.CatalogPlan {
	p := billing.LookupStaticPlan(slug)
	if p == nil {
		return nil
	}
	return &grant.CatalogPlan{
		Slug:        p.Slug,
		Name:        p.Name,
		Description: p.Description,
		PriceCents:  p.PriceMonth,
		Currency:    firstNonEmpty(p.Currency, "usd"),
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func defaultGrantedBy() string {
	if u := os.Getenv("USER"); u != "" {
		return u + "@cli"
	}
	return "commerce-grant"
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "commerce-grant: "+strings.TrimSuffix(format, "\n")+"\n", args...)
	os.Exit(1)
}
