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
	if Need() != Events.Ceiling+Usage.Ceiling {
		t.Fatalf("Need() = %d, which is not every ceiling", Need())
	}
}

func TestWhatAStreamReserves(t *testing.T) {
	for _, c := range []struct {
		name  string
		r     Reservation
		store int64
		want  int64
	}{
		{"production keeps the event ceiling", Events, 18 * gib, 8 * gib},
		{"production keeps the usage ceiling", Usage, 18 * gib, gib},
		{"a small store holds a share of events", Events, 4 * gib, 2 * gib},
		{"a small store holds a share of usage", Usage, 4*gib + 30, 4*gib/10 + 3},
		{"an unlimited account keeps the ceiling", Events, -1, 8 * gib},
		{"an unreported store keeps the ceiling", Events, 0, 8 * gib},
	} {
		if got := c.r.Of(c.store); got != c.want {
			t.Errorf("%s: of a %d store = %d, want %d", c.name, c.store, got, c.want)
		}
	}
}

// On any store that limits them, the reservations fit beside each other.
func TestTheReservationsFitTogether(t *testing.T) {
	for _, store := range []int64{1, 99, 64 << 20, gib, 2 * gib, 4 * gib, 9 * gib, 1 << 60} {
		var sum int64
		for _, r := range Reservations {
			sum += r.Of(store)
		}
		if sum > store {
			t.Errorf("a %d store is asked to reserve %d", store, sum)
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
		want                 int64
	}{
		{"nothing reserved: its share", 2048 * mib, 0, 0, 1024 * mib},
		{"the others at their shares: its share", 2048 * mib, 204 * mib, 0, 1024 * mib},
		{"another holds an old ceiling: what is left", 1390 * mib, 1024 * mib, 0, 366 * mib},
		{"resizing itself beside that one: what is left", 1390 * mib, 2048 * mib, 1024 * mib, 366 * mib},
		{"resizing itself, alone: its share", 1390 * mib, 1024 * mib, 1024 * mib, 695 * mib},
		{"nothing left: its share, for the bus to refuse", 1390 * mib, 4096 * mib, 0, 695 * mib},
		{"an unlimited account: its ceiling", -1, 4096 * mib, 0, 8 * gib},
	} {
		if got := Events.Within(c.store, c.reserved, c.own); got != c.want {
			t.Errorf("%s: Within(%d, %d, %d) = %d, want %d", c.name, c.store, c.reserved, c.own, got, c.want)
		}
	}
}
