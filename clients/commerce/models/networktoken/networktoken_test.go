package networktoken

import "testing"

func TestSuspend(t *testing.T) {
	nt := &NetworkToken{Status: Active}
	if err := nt.Suspend(); err != nil {
		t.Fatalf("Suspend failed: %v", err)
	}
	if nt.Status != Suspended {
		t.Errorf("expected suspended, got %s", nt.Status)
	}
}

func TestSuspendInvalid(t *testing.T) {
	nt := &NetworkToken{Status: Deleted}
	if err := nt.Suspend(); err == nil {
		t.Error("expected error suspending deleted token")
	}
}

func TestResume(t *testing.T) {
	nt := &NetworkToken{Status: Suspended}
	if err := nt.Resume(); err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if nt.Status != Active {
		t.Errorf("expected active, got %s", nt.Status)
	}
}

func TestResumeInvalid(t *testing.T) {
	nt := &NetworkToken{Status: Active}
	if err := nt.Resume(); err == nil {
		t.Error("expected error resuming active token")
	}
}

func TestMarkDeleted(t *testing.T) {
	nt := &NetworkToken{Status: Active}
	if err := nt.MarkDeleted(); err != nil {
		t.Fatalf("MarkDeleted failed: %v", err)
	}
	if nt.Status != Deleted {
		t.Errorf("expected deleted, got %s", nt.Status)
	}
}

func TestMarkDeletedAlready(t *testing.T) {
	nt := &NetworkToken{Status: Deleted}
	if err := nt.MarkDeleted(); err == nil {
		t.Error("expected error deleting already deleted token")
	}
}

func TestIsUsable(t *testing.T) {
	tests := []struct {
		status Status
		usable bool
	}{
		{Active, true},
		{Suspended, false},
		{Deleted, false},
	}

	for _, tt := range tests {
		nt := &NetworkToken{Status: tt.status}
		if got := nt.IsUsable(); got != tt.usable {
			t.Errorf("IsUsable() for %s = %v, want %v", tt.status, got, tt.usable)
		}
	}
}
