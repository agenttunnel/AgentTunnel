package steering

import (
	"reflect"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

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
	if len(got) != len(want) {
		t.Fatalf("drained = %+v", got)
	}
	for i, e := range got {
		if e.MessageID != want[i].MessageID || e.Text != want[i].Text || len(e.Images) != 0 {
			t.Fatalf("drained = %+v", got)
		}
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

func TestPostCarriesImages(t *testing.T) {
	b := New()
	imgs := []providers.ImageInput{{Path: "/att/a.png", Mime: "image/png"}}
	b.Post("t1", "m1", "look at this", imgs...)

	items := b.Drain("t1")
	if len(items) != 1 || items[0].MessageID != "m1" || !reflect.DeepEqual(items[0].Images, imgs) {
		t.Fatalf("drained = %+v", items)
	}

	// An image with no text still steers: the pass sees the attachment.
	b.Post("t2", "m2", "", imgs...)
	if items := b.Drain("t2"); len(items) != 1 || len(items[0].Images) != 1 {
		t.Fatalf("image-only steering was dropped: %+v", items)
	}
}
