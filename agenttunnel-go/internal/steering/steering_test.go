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

	b.Post("t1", "  focus on the parser  ")
	b.Post("t1", "also add a test")
	b.Post("t1", "   ") // blank is ignored

	if !b.Has("t1") {
		t.Fatal("Has should see the queued messages")
	}

	got := b.Drain("t1")
	if len(got) != 2 || got[0].Text != "focus on the parser" || got[1].Text != "also add a test" {
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

func TestPostCarriesImages(t *testing.T) {
	b := New()
	imgs := []providers.ImageInput{{Path: "/att/a.png", Mime: "image/png"}}
	b.Post("t1", "look at this", imgs...)

	items := b.Drain("t1")
	if len(items) != 1 || !reflect.DeepEqual(items[0].Images, imgs) {
		t.Fatalf("drained = %+v", items)
	}

	// An image with no text still steers: the pass sees the attachment.
	b.Post("t2", "", imgs...)
	if items := b.Drain("t2"); len(items) != 1 || len(items[0].Images) != 1 {
		t.Fatalf("image-only steering was dropped: %+v", items)
	}
}
