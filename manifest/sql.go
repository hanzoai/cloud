package manifest

// sql.go — how a replica's connections to hanzo-sql are divided among its processes.
//
// A replica is not one process. The host spawns one child per subsystem, each child is
// a binary with its own connection budget (the driver) and its own administrative pool,
// and they all inherit the host's environment. A budget of 50 stated once in that
// environment is therefore 50 per child, and the three children that keep stores on
// hanzo-sql would hold 150 connections per replica and 450 for three replicas, beside
// everything else that uses the server.
//
// So CLOUD_SQL_CONNS, in the host's environment, is the REPLICA's budget, and the host
// hands each child its share through the child's own environment. A child that opens
// stores on hanzo-sql gets an equal part of it; a child that opens none is given one
// connection, which is all its registry reads need and what a mistake would cost. The
// arithmetic a deployment answers to is in LLM.md: replicas x CLOUD_SQL_CONNS, plus
// every other client of the server, within its max_connections less headroom.
//
// The names below are the driver's (Env, ConnsEnv). This package imports nothing but zip,
// so they are spelled here, and a test holds the two spellings together.

import (
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// SQLStores names, for each subsystem whose stores run on hanzo-sql, the app whose
// process opens them: the customer books (finance, finance-test) are opened by the
// commerce app, the house book by treasury, the definitions by the flag app. It is the one
// list of what is ported; with the DSN set the root package's OrgDB keeps any other
// subsystem on its file, and the host divides the connection budget among the apps
// named here. A port adds its subsystem in the commit that lands it.
var SQLStores = map[string]string{
	"account":            "team",
	"ads":                "ad",
	"affiliates":         "affiliate",
	"agent":              "agent",
	"agent-shares":       "agent",
	"ai":                 "ai",
	"agents":             "agent",
	"authors":            "author",
	"automations":        "auto",
	"base":               "base",
	"base-aux":           "base",
	"benchmark":          "benchmark",
	"books":              "finance",
	"books-sandbox":      "finance",
	"campaign":           "campaign",
	"captable":           "captable",
	"channels":           "transport",
	"code":               "code",
	"commerce":           "commerce",
	"commerce-base-aux":  "commerce",
	"commerce-base-data": "commerce",
	"company":            "company",
	"compliance":         "compliance",
	"dataroom":           "dataroom",
	"destinations":       "destination",
	"dev":                "sandbox",
	"docs":               "team",
	"domains":            "domain",
	"entitlements":       "entitlement",
	"environment":        "environment",
	"esign":              "esign",
	"evals":              "eval",
	"experiments":        "experiment",
	"finance":            "commerce",
	"finance-test":       "commerce",
	"flags":              "flag",
	"forge_metrics":      "git",
	"functions":          "function",
	"gateway":            "gateway",
	"git":                "git",
	"graph":              "graph",
	"guide":              "guide",
	"guide-blueprint":    "guide",
	"index":              "index",
	"ingress":            "ingress",
	"integrations":       "provider",
	"kms":                "kms",
	"knowledge":          "knowledge",
	"label":              "label",
	"leaderboard":        "leaderboard",
	"legal":              "legal",
	"limits":             "ai",
	"link":               "link",
	"link_index":         "dataroom",
	"marketplace":        "marketplace",
	"o11y":               "o11y",
	"o11y_annotations":   "o11y",
	"objects":            "git",
	"ssh_keys":           "git",
	"patent":             "patent",
	"platform":           "platform",
	"prefs":              "pref",
	"principal":          "principal",
	"projects":           "project",
	"prompts":            "prompt",
	"provisioning":       "provisioning",
	"public_rooms":       "team",
	"reference":          "reference",
	"referrals":          "referral",
	"requests":           "agent",
	"research":           "research",
	"reward":             "reward",
	"sandbox":            "sandbox",
	"security":           "security",
	"cyber":              "cyber",
	"settings":           "settings",
	"social":             "social",
	"sources":            "sources",
	"sync":               "sync",
	"tasks-account":      "account",
	"tasks-ad":           "ad",
	"tasks-analytics":    "analytics",
	"tasks-admission":    "admission",
	"tasks-affiliate":    "affiliate",
	"tasks-agent":        "agent",
	"tasks-ai":           "ai",
	"tasks-allowance":    "allowance",
	"tasks-amqp":         "amqp",
	"tasks-ask":          "ask",
	"tasks-audit":        "audit",
	"tasks-author":       "author",
	"tasks-authz":        "authz",
	"tasks-auto":         "auto",
	"tasks-base":         "base",
	"tasks-benchmark":    "benchmark",
	"tasks-billing":      "billing",
	"tasks-blueprint":    "blueprint",
	"tasks-finance":      "finance",
	"tasks-bot":          "bot",
	"tasks-campaign":     "campaign",
	"tasks-captable":     "captable",
	"tasks-catalog":      "catalog",
	"tasks-ci":           "ci",
	"tasks-cloudflare":   "cloudflare",
	"tasks-code":         "code",
	"tasks-commerce":     "commerce",
	"tasks-company":      "company",
	"tasks-compliance":   "compliance",
	"tasks-compute":      "compute",
	"tasks-content":      "content",
	"tasks-crawl":        "crawl",
	"tasks-dataroom":     "dataroom",
	"tasks-dataset":      "dataset",
	"tasks-deploy":       "deploy",
	"tasks-sources":      "sources",
	"tasks-destination":  "destination",
	"tasks-dns":          "dns",
	"tasks-domain":       "domain",
	"tasks-email":        "email",
	"tasks-engine":       "engine",
	"tasks-entitlement":  "entitlement",
	"tasks-environment":  "environment",
	"tasks-esign":        "esign",
	"tasks-eval":         "eval",
	"tasks-event":        "event",
	"tasks-exec":         "exec",
	"tasks-experiment":   "experiment",
	"tasks-explorer":     "explorer",
	"tasks-flag":         "flag",
	"tasks-flow":         "flow",
	"tasks-form":         "form",
	"framework":          "framework",
	"tasks-framework":    "framework",
	"tasks-function":     "function",
	"tasks-gateway":      "gateway",
	"tasks-git":          "git",
	"tasks-graph":        "graph",
	"tasks-guide":        "guide",
	"tasks-help":         "help",
	"tasks-iam":          "iam",
	"tasks-index":        "index",
	"tasks-ingress":      "ingress",
	"tasks-kafka":        "kafka",
	"tasks-kms":          "kms",
	"tasks-knowledge":    "knowledge",
	"tasks-kv":           "kv",
	"tasks-label":        "label",
	"tasks-leaderboard":  "leaderboard",
	"tasks-legal":        "legal",
	"tasks-library":      "library",
	"tasks-link":         "link",
	"tasks-lsp":          "lsp",
	"tasks-machines":     "machines",
	"tasks-market":       "market",
	"tasks-marketplace":  "marketplace",
	"tasks-meet":         "meet",
	"tasks-metrics":      "metrics",
	"tasks-ml":           "ml",
	"tasks-mq":           "mq",
	"tasks-network":      "network",
	"tasks-node":         "node",
	"tasks-notify":       "notify",
	"tasks-o11y":         "o11y",
	"tasks-patent":       "patent",
	"tasks-patrol":       "patrol",
	"tasks-platform":     "platform",
	"tasks-plugins":      "plugins",
	"tasks-pref":         "pref",
	"tasks-principal":    "principal",
	"tasks-project":      "project",
	"tasks-prompt":       "prompt",
	"tasks-provider":     "provider",
	"tasks-provisioning": "provisioning",
	"tasks-pubsub":       "pubsub",
	"tasks-reference":    "reference",
	"tasks-referral":     "referral",
	"tasks-registry":     "registry",
	"tasks-research":     "research",
	"tasks-reward":       "reward",
	"tasks-s3":           "s3",
	"tasks-sandbox":      "sandbox",
	"tasks-sbom":         "sbom",
	"tasks-search":       "search",
	"tasks-security":     "security",
	"tasks-cyber":        "cyber",
	"tasks-seo":          "seo",
	"tasks-settings":     "settings",
	"tasks-share":        "share",
	"tasks-skills":       "skills",
	"tasks-social":       "social",
	"tasks-space":        "space",
	"tasks-standing":     "standing",
	"tasks-studio":       "studio",
	"tasks-sync":         "sync",
	"tasks-task":         "task",
	"tasks-tax":          "tax",
	"tasks-taxonomy":     "taxonomy",
	"tasks-team":         "team",
	"tasks-tel":          "tel",
	"tasks-template":     "template",
	"tasks-tool":         "tool",
	"tasks-train":        "train",
	"tasks-translate":    "translate",
	"tasks-transport":    "transport",
	"tasks-treasury":     "treasury",
	"tasks-triage":       "triage",
	"tasks-trust":        "trust",
	"tasks-usage":        "usage",
	"tasks-validator":    "validator",
	"tasks-wallet":       "wallet",
	"tasks-web3":         "web3",
	"tasks-webhook":      "webhook",
	"tasks-websearch":    "websearch",
	"tasks-workflow":     "workflow",
	"tasks-world":        "world",
	"tasks-worlds":       "worlds",
	"tasks-x402":         "x402",
	"tax":                "tax",
	"taxonomy":           "taxonomy",
	"tel":                "tel",
	"templates":          "template",
	"token_index":        "esign",
	"tools-activation":   "tool",
	"tools-catalog":      "tool",
	"tools-mcp":          "tool",
	"tools-plugins":      "tool",
	"tools-skills":       "tool",
	"tracker":            "task",
	"train":              "train",
	"translate":          "translate",
	"treasury":           "treasury",
	"triage":             "triage",
	"trust":              "trust",
	"validators":         "validator",
	"waitlist":           "admission",
	"wallets":            "wallet",
	"webhooks":           "webhook",
	"world":              "world",
	"x402":               "x402",
}

const (
	sqlDSNEnv   = "CLOUD_SQL_DSN"
	sqlConnsEnv = "CLOUD_SQL_CONNS"
	// sqlConnsDefault is the replica's budget when none is stated.
	sqlConnsDefault = 50
	// sqlShareFloor is the least a child that opens stores can work on: an administrative
	// connection, a store's, and the one a migration holds.
	sqlShareFloor = 4
)

// SQLOwners is the apps that open stores on hanzo-sql, sorted, once each.
func SQLOwners() []string {
	var out []string
	for _, app := range SQLStores {
		if !slices.Contains(out, app) {
			out = append(out, app)
		}
	}
	sort.Strings(out)
	return out
}

// sqlEnv is the CLOUD_SQL_CONNS this app's child is started with, or nothing when the
// deployment keeps no stores on hanzo-sql.
func (a App) sqlEnv() []string {
	if strings.TrimSpace(os.Getenv(sqlDSNEnv)) == "" {
		return nil
	}
	total := sqlConnsDefault
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(sqlConnsEnv))); err == nil && n > 0 {
		total = n
	}
	owners := SQLOwners()
	share := 1
	if slices.Contains(owners, a.Name) {
		share = max(sqlShareFloor, total/len(owners))
	}
	return []string{sqlConnsEnv + "=" + strconv.Itoa(share)}
}
