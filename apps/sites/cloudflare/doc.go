// Package cloudflare purges the Cloudflare edge cache by cache-tag, so a site
// redeploy is live at the edge at once. Its credentials come only from the
// environment the operator injects from KMS (CF_API_TOKEN, CF_ZONE_ID).
package cloudflare
