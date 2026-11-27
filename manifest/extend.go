package manifest

// Register adds or updates subsystem rows on Apps (preserving order for
// replacements and appending new rows before "ai" by default so specific
// prefixes precede ai's "/v1" remainder, or at the tail for Coresident rows),
// registers their embedded tasks engines in SQLStores, and rebuilds the
// singular/plural path alias table.
//
// Private or custom distributions call Register (or InsertBefore) from their
// thin composition entrypoint before host.Main().
func Register(apps ...App) {
	for _, a := range apps {
		if a.Name == "" {
			continue
		}
		replaced := false
		for i := range Apps {
			if Apps[i].Name == a.Name {
				Apps[i] = a
				replaced = true
				break
			}
		}
		if !replaced {
			idx := -1
			if !a.Coresident {
				for i, cur := range Apps {
					if cur.Name == "ai" {
						idx = i
						break
					}
				}
			}
			if idx >= 0 {
				Apps = append(Apps[:idx], append([]App{a}, Apps[idx:]...)...)
			} else {
				Apps = append(Apps, a)
			}
		}
		if !a.Coresident {
			SQLStores["tasks-"+a.Name] = a.Name
		}
	}
	rebuildAliases()
}

// InsertBefore inserts apps immediately before the named target app in Apps (or
// falls back to Register when target is not present), keeping exact canonical
// mount order for distributions that layer private plugins onto the OSS fleet.
func InsertBefore(target string, apps ...App) {
	if len(apps) == 0 {
		return
	}
	filtered := make([]App, 0, len(Apps))
	adding := map[string]bool{}
	for _, a := range apps {
		adding[a.Name] = true
	}
	for _, cur := range Apps {
		if !adding[cur.Name] {
			filtered = append(filtered, cur)
		}
	}
	idx := -1
	for i, cur := range filtered {
		if cur.Name == target {
			idx = i
			break
		}
	}
	if idx < 0 {
		Apps = filtered
		Register(apps...)
		return
	}
	out := make([]App, 0, len(filtered)+len(apps))
	out = append(out, filtered[:idx]...)
	out = append(out, apps...)
	out = append(out, filtered[idx:]...)
	Apps = out
	for _, a := range apps {
		if !a.Coresident {
			SQLStores["tasks-"+a.Name] = a.Name
		}
	}
	rebuildAliases()
}

// RegisterSQLStores merges additional store -> app ownership mappings into
// SQLStores for hanzo-sql connection budgeting.
func RegisterSQLStores(stores map[string]string) {
	for k, v := range stores {
		SQLStores[k] = v
	}
}
