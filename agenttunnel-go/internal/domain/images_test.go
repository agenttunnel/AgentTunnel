package domain

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// A turn with images must survive the event log: the payload carries meta
// only, and the meta replays into the read model unchanged.
func TestImagesReplayThroughFileStore(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e := NewEngine(store, nil)
	e.Start(ctx)
	t.Cleanup(e.Close)

	mustDispatch(t, e, ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1"})
	mustDispatch(t, e, ctx, contracts.Command{
		ID: "c2", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "what is in this picture?",
		Images: []contracts.ImageRef{
			{ID: "0123456789abcdef0123456789abcdef", Name: "bug.png", Mime: "image/png", Size: 2048, Width: 800, Height: 600},
			{ID: "fedcba9876543210fedcba9876543210", Name: "trace.gif", Mime: "image/gif", Size: 4096},
		},
	})
	e.Close()

	// A fresh engine replays the log from disk.
	reopened, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	e2 := NewEngine(reopened, nil)
	e2.Start(ctx)
	t.Cleanup(e2.Close)

	st, err := e2.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) != 1 {
		t.Fatalf("messages = %d", len(st.Messages))
	}
	msg := st.Messages[0]
	if len(msg.Images) != 2 {
		t.Fatalf("images = %+v", msg.Images)
	}
	if msg.Images[0].Name != "bug.png" || msg.Images[0].Width != 800 || msg.Images[0].Height != 600 {
		t.Fatalf("image[0] = %+v", msg.Images[0])
	}
	if msg.Images[1].Mime != "image/gif" || msg.Images[1].Width != 0 {
		t.Fatalf("image[1] = %+v", msg.Images[1])
	}

	// The persisted event lines hold meta, never bytes: a 2 KiB upload must
	// not have written a multi-kilobyte payload.
	raw, err := os.ReadFile(filepath.Join(dir, "t1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 2048 {
		t.Fatalf("event log grew to %d bytes; images look inlined", len(raw))
	}

	// A clone deep-copies the slice so a caller cannot reach back into it.
	clone := st.Clone()
	clone.Messages[0].Images[0].Name = "mutated"
	if again, _ := e2.State(ctx, "t1"); again.Messages[0].Images[0].Name != "bug.png" {
		t.Fatal("clone shares state with the engine")
	}
}

func TestDeciderRejectsImageCapViolations(t *testing.T) {
	valid := contracts.ImageRef{ID: "0123456789abcdef", Mime: "image/png", Size: 512}
	s := NewThreadState("t1")
	s.Exists = true

	cases := []struct {
		name   string
		images []contracts.ImageRef
	}{
		{"too many", func() []contracts.ImageRef {
			imgs := make([]contracts.ImageRef, contracts.MaxTurnImages+1)
			for i := range imgs {
				imgs[i] = valid
			}
			return imgs
		}()},
		{"bad id", []contracts.ImageRef{{ID: "../etc/passwd", Mime: "image/png", Size: 12}}},
		{"bad mime", []contracts.ImageRef{{ID: "0123456789abcdef", Mime: "image/bmp", Size: 12}}},
		{"empty size", []contracts.ImageRef{{ID: "0123456789abcdef", Mime: "image/png"}}},
		{"oversize", []contracts.ImageRef{{ID: "0123456789abcdef", Mime: "image/png", Size: contracts.MaxImageBytes + 1}}},
		{"over per-turn total", []contracts.ImageRef{
			{ID: "0123456789abcdef", Mime: "image/png", Size: contracts.MaxImageBytes},
			{ID: "0123456789fedcba", Mime: "image/png", Size: contracts.MaxImageBytes},
			{ID: "0123456789abcdef", Mime: "image/png", Size: contracts.MaxImageBytes},
			{ID: "0123456789abcdef", Mime: "image/png", Size: contracts.MaxImageBytes},
			{ID: "0123456789abcdef", Mime: "image/png", Size: 1},
		}},
	}
	for _, tc := range cases {
		_, err := Decide(contracts.Command{
			ID: "c", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "hi", Images: tc.images,
		}, s)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}

	// The message-append path (steering) carries the same gate.
	_, err := Decide(contracts.Command{
		ID: "c", Type: contracts.CmdMessageAppend, ThreadID: "t1",
		Message: &contracts.MessageSent{
			MessageID: "m", Author: "steering", Text: "hi",
			Images: []contracts.ImageRef{{ID: "zz", Mime: "image/png", Size: 10}},
		},
	}, s)
	if err == nil {
		t.Error("append with invalid image id accepted")
	}
}
