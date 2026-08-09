package visor

import "testing"

// A machine's name and size are FREE TEXT a caller types, forwarded to Visor and
// echoed back into the reply we render. So each of these views chooses between a
// caller's word and a real identifier, and a word that is only whitespace must
// lose that choice — it is not a value.
//
// These are the inputs that separate the two readings, and each one reached a
// visible surface: a machine whose id became "   ", a size that parsed to zero
// vcpu, a cluster named "\t", and a URL path with %20 where a machine id belongs.

func TestABlankNameLosesToTheRealID(t *testing.T) {
	got := toMachineView(visorMachine{Name: "   ", Id: "droplet-123"})
	if got.ID != "droplet-123" {
		t.Errorf("ID = %q, want droplet-123 — a name of spaces is not a name", got.ID)
	}
}

func TestABlankSizeLosesToTheRealType(t *testing.T) {
	got := toMachineView(visorMachine{Id: "m1", Size: "   ", Type: "s-4vcpu-8gb"})
	if got.Type != "s-4vcpu-8gb" {
		t.Fatalf("Type = %q, want s-4vcpu-8gb", got.Type)
	}
	// The type is what the spec is derived from, so losing it costs the whole shape.
	if got.Vcpu == nil || *got.Vcpu != 4 {
		t.Errorf("Vcpu = %v, want 4 — the size slug decides the spec", got.Vcpu)
	}
}

func TestABlankClusterIDLosesToTheName(t *testing.T) {
	got := clustersFromPools([]visorNodePool{{ClusterID: "\t", Name: "gpu"}})
	if len(got) != 1 {
		t.Fatalf("got %d clusters, want 1", len(got))
	}
	if got[0].Name != "gpu" {
		t.Errorf("cluster Name = %q, want gpu — a tab is not an id", got[0].Name)
	}
}

func TestAPoolWithNothingToNameItIsDropped(t *testing.T) {
	// Both candidates blank: there is no pool to speak of, and the guard that drops
	// it only fires if blank reads as blank.
	if got := clustersFromPools([]visorNodePool{{ClusterID: " ", Name: "\t"}}); len(got) != 0 {
		t.Errorf("got %d clusters, want none", len(got))
	}
}
