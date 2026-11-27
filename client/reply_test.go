package client

import "testing"

// A thread's address survives being a room, and a room that is not a
// conversation is not read as one.
func TestReplyRoom(t *testing.T) {
	to := Reply{Provider: "slack", Channel: "C024BE91L", Thread: "1726459200.001900", User: "U1", Turn: "t"}
	back, ok := ReplyTo(to.Address())
	if !ok || back != (Reply{Provider: "slack", Channel: "C024BE91L", Thread: "1726459200.001900"}) {
		t.Fatalf("round trip = %+v %v", back, ok)
	}
	if (Reply{Provider: "slack"}).Address() != "" {
		t.Fatal("an address with no channel is no room")
	}
	for _, room := range []string{"", "space_room", "slack:", ":C1/1.2"} {
		if _, ok := ReplyTo(room); ok {
			t.Errorf("%q read as a conversation", room)
		}
	}
}

// An issue's run is its own words, closing it, where it is discussed.
func TestWork(t *testing.T) {
	in := Work("api", 7, " the ask ", "what it needs\n", Reply{Channel: "C1"})
	if in.Prompt != "the ask\n\nwhat it needs" || in.Issue != "7" || in.Repo != "api" || in.Reply.Channel != "C1" {
		t.Fatalf("work = %+v", in)
	}
	if Work("api", 0, "the ask", "", Reply{}).Issue != "" {
		t.Fatal("work no issue asked for names one")
	}
	if Work("api", 7, "the ask", "", Reply{}).Prompt != "the ask" {
		t.Fatal("an issue with no body is its title")
	}
}
