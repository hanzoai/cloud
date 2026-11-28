package bus

import "testing"

const gib = int64(1) << 30

// The reserving streams must fit the store together, whatever its size: a table
// that adds up to more than the whole is one whose last stream is refused.
func TestTheSharesFitTheStore(t *testing.T) {
	for _, r := range Reservations {
		if r.Share <= 0 || r.Ceiling <= 0 {
			t.Fatalf("%+v reserves nothing", r)
		}
	}
	if Reserved() > 100 {
		t.Fatalf("the shares add up to %d%% of the store", Reserved())
	}
	if Need(1) != Events.Ceiling+Usage.Ceiling {
		t.Fatalf("Need(1) = %d, which is not every ceiling", Need(1))
	}
	if Need(3) != 3*(Events.Ceiling+Usage.Ceiling) {
		t.Fatalf("Need(3) = %d, want %d across three replicas", Need(3), 3*(Events.Ceiling+Usage.Ceiling))
	}
}

func TestWhatAStreamReserves(t *testing.T) {
	for _, c := range []struct {
		name     string
		r        Reservation
		store    int64
		replicas int
		want     int64
	}{
		{"production keeps the event ceiling", Events, 18 * gib, 1, 8 * gib},
		{"production keeps the usage ceiling", Usage, 18 * gib, 1, gib},
		{"a small store holds a share of events", Events, 4 * gib, 1, 2 * gib},
		{"a small store holds a share of usage", Usage, 4*gib + 30, 1, 4*gib/10 + 3},
		{"zero replicas normalizes to single copy", Events, 4 * gib, 0, 2 * gib},
		{"three replicas divide the account share for events", Events, 12 * gib, 3, 2 * gib},
		{"three replicas divide the account share for usage", Usage, 30 * gib, 3, gib},
		{"three replicas cap an 18 GiB account below the 8 GiB ceiling", Events, 18 * gib, 3, 3 * gib},
		{"three replicas reach the ceiling once the account holds 48 GiB", Events, 60 * gib, 3, 8 * gib},
		{"an unlimited account keeps the ceiling at one replica", Events, -1, 1, 8 * gib},
		{"an unlimited account keeps the ceiling at three replicas", Events, -1, 3, 8 * gib},
		{"an unreported store keeps the ceiling", Events, 0, 3, 8 * gib},
	} {
		if got := c.r.Of(c.store, c.replicas); got != c.want {
			t.Errorf("%s: of a %d store (replicas=%d) = %d, want %d", c.name, c.store, c.replicas, got, c.want)
		}
	}
}

// On any store that limits them, the reservations fit beside each other once
// JetStream multiplies each stream's MaxBytes by its replica count.
func TestTheReservationsFitTogether(t *testing.T) {
	for _, replicas := range []int{1, 3, 5} {
		for _, store := range []int64{1, 99, 64 << 20, gib, 2 * gib, 4 * gib, 9 * gib, 18 * gib, 60 * gib, 1 << 60} {
			var sum int64
			for _, r := range Reservations {
				sum += r.Of(store, replicas) * int64(replicas)
			}
			if sum > store {
				t.Errorf("replicas=%d: a %d store is asked to reserve %d", replicas, store, sum)
			}
		}
	}
}

// A store carried over from another budget: one stream still holds more than its
// share, and the other takes what is left rather than being refused its share.
func TestAStreamTakesWhatTheOthersLeave(t *testing.T) {
	const mib = int64(1) << 20
	for _, c := range []struct {
		name                 string
		store, reserved, own int64
		replicas             int
		want                 int64
	}{
		{"nothing reserved: its share", 2048 * mib, 0, 0, 1, 1024 * mib},
		{"the others at their shares: its share", 2048 * mib, 204 * mib, 0, 1, 1024 * mib},
		{"another holds an old ceiling: what is left", 1390 * mib, 1024 * mib, 0, 1, 366 * mib},
		{"resizing itself beside that one: what is left", 1390 * mib, 2048 * mib, 1024 * mib, 1, 366 * mib},
		{"resizing itself, alone: its share", 1390 * mib, 1024 * mib, 1024 * mib, 1, 695 * mib},
		{"nothing left: its share, for the bus to refuse", 1390 * mib, 4096 * mib, 0, 1, 695 * mib},
		{"an unlimited account: its ceiling", -1, 4096 * mib, 0, 1, 8 * gib},
		{"replicas=3, nothing reserved: its per-replica share", 3072 * mib, 0, 0, 3, 512 * mib},
		{"replicas=3, others at their share: its per-replica share", 3072 * mib, 102 * mib, 0, 3, 512 * mib},
		{"replicas=3, another holds 300 MiB/replica on 1200 MiB store: takes 100 MiB/replica", 1200 * mib, 300 * mib, 0, 3, 100 * mib},
		{"replicas=3, resizing itself beside 300 MiB/replica on 1200 MiB store: takes 100 MiB/replica", 1200 * mib, 500 * mib, 200 * mib, 3, 100 * mib},
		{"replicas=3, nothing left: its per-replica share, for the bus to refuse", 1200 * mib, 500 * mib, 0, 3, 200 * mib},
		{"replicas=3, an unlimited account: its ceiling", -1, 4096 * mib, 0, 3, 8 * gib},
	} {
		if got := Events.Within(c.store, c.reserved, c.own, c.replicas); got != c.want {
			t.Errorf("%s: Within(%d, %d, %d, %d) = %d, want %d", c.name, c.store, c.reserved, c.own, c.replicas, got, c.want)
		}
	}
}
