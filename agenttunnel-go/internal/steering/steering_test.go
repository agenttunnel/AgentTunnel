package steering

import "testing"

func TestPostDrainAndHas(t *testing.T) {
	b := New()
	if b.Has("t1") {
		t.Fatal("empty queue should report no steering")
	}

	b.Post("t1", "  focus on the parser  ")
	b.Post("t1", "also add a test")
	b.Post("t1", "   ") // blank is ignored

	if !b.Has("t1") {
		t.Fatal("Has should see the queued messages")
	}

	got := b.Drain("t1")
	if len(got) != 2 || got[0] != "focus on the parser" || got[1] != "also add a test" {
		t.Fatalf("drained = %+v", got)
	}
	if b.Has("t1") {
		t.Fatal("drain should clear the queue")
	}
}

func TestPostIgnoresEmptyThread(t *testing.T) {
	b := New()
	b.Post("", "hello")
	if b.Has("") {
		t.Fatal("a message with no thread should be dropped")
	}
}
