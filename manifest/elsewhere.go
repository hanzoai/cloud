package manifest

// Elsewhere returns the prefixes the fleet delivers to somebody OTHER than name.
//
// It exists for the one app whose prefix is not a namespace but a REMAINDER. ai's
// row is `/v1` and it is mounted last, so what it actually answers is "everything
// under /v1 that no earlier app claimed" — 15 of the routes its own router
// registers are delivered to a sibling instead, and four of them (/v1/metrics,
// /v1/index, /v1/admin/providers, /v1/scrape/preview) answer 404 on api.hanzo.ai
// today because the sibling that receives them does not serve them.
//
// An app that published those would publish phantoms: paths in the contract that
// no request reaches, which is precisely the defect a router-derived document
// exists to make impossible. So the app asks which of its routes are not its own,
// and the answer comes from Apps — the one routing table — rather than from a
// second list beside it.
//
// EARLIER ROWS ONLY, because that is how the host routes: it loads the apps in
// this order and the router takes the first prefix that matches, which is why
// account's /v1/iam/keys must precede iam's /v1/iam. An app later in the list
// cannot take a path from an earlier one, so it is not "elsewhere" for it.
// Coresident apps are skipped for the reason they are skipped everywhere: they
// claim no prefix.
//
// This is a derivation of Apps, not a second router. manifest/router_test.go
// builds the REAL router from the same table and asks it where each published path
// goes, so a wrong derivation here does not ship — it turns that gate red.
func Elsewhere(name string) []string {
	var out []string
	for _, a := range Apps {
		if a.Name == name {
			return out
		}
		if a.Coresident {
			continue
		}
		out = append(out, a.Prefixes...)
	}
	return nil // an unknown name claims nothing, so nothing is elsewhere for it
}
