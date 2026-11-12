package steering

import "testing"

func TestPostDrainAndHas(t *testing.T) {
	b := New()
	if b.Has("t1") {
		t.Fatal("empty queue should report no steering")
	}

	b.Post("t1", "m1", "  focus on the parser  ")
	b.Post("t1", "m2", "also add a test")
	b.Post("t1", "m3", "   ") // blank is ignored

	if !b.Has("t1") {
		t.Fatal("Has should see the queued messages")
	}

	got := b.Drain("t1")
	want := []Entry{
		{MessageID: "m1", Text: "focus on the parser"},
		{MessageID: "m2", Text: "also add a test"},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("drained = %+v", got)
	}
	if b.Has("t1") {
		t.Fatal("drain should clear the queue")
	}
}

func TestPostIgnoresEmptyThread(t *testing.T) {
	b := New()
	b.Post("", "m1", "hello")
	if b.Has("") {
		t.Fatal("a message with no thread should be dropped")
	}
}
