package k8s

// Disk pressure is a node fact the disk guard writes (hanzoai/k8s disk/, universe
// infra/aws/k8s/disk.yaml): when a node's root or storage disk passes 80% it
// labels the node DiskLabel=DiskPressure, and below 75% it removes the label.
const (
	DiskLabel    = "hanzo.ai/disk"
	DiskPressure = "pressure"
)

// AvoidPressure keeps pod — an unstructured pod spec — off a node under disk
// pressure. Every pod that writes heavily to its node's disk calls it: builds,
// CI runners, sandboxes.
//
// It adds `DiskLabel NotIn [DiskPressure]` to EVERY term of the pod's required
// node affinity, creating one term when there is none: terms are ORed, so a
// requirement missing from one of them does not hold. A node without the label
// passes, so a node the guard has not reached yet still takes work.
func AvoidPressure(pod map[string]any) {
	affinity := child(pod, "affinity")
	node := child(affinity, "nodeAffinity")
	required := child(node, "requiredDuringSchedulingIgnoredDuringExecution")
	terms, _ := required["nodeSelectorTerms"].([]any)
	if len(terms) == 0 {
		terms = []any{map[string]any{}}
	}
	for _, t := range terms {
		term, ok := t.(map[string]any)
		if !ok {
			continue
		}
		exprs, _ := term["matchExpressions"].([]any)
		if !avoids(exprs) {
			term["matchExpressions"] = append(exprs, map[string]any{
				"key": DiskLabel, "operator": "NotIn", "values": []any{DiskPressure},
			})
		}
	}
	required["nodeSelectorTerms"] = terms
}

func child(m map[string]any, key string) map[string]any {
	c, ok := m[key].(map[string]any)
	if !ok {
		c = map[string]any{}
		m[key] = c
	}
	return c
}

func avoids(exprs []any) bool {
	for _, e := range exprs {
		if m, ok := e.(map[string]any); ok && m["key"] == DiskLabel && m["operator"] == "NotIn" {
			return true
		}
	}
	return false
}
