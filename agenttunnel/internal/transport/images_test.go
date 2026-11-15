package transport

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/attachments"
	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// pngBytes renders a small PNG, the smallest upload the store accepts.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// imageDispatcher wires the pieces imageInputs and runTurn touch before any
// turn runs: an engine with one thread and a live attachment store.
func imageDispatcher(t *testing.T) (*domain.Engine, *Dispatcher) {
	t.Helper()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(context.Background())
	t.Cleanup(eng.Close)
	disp := NewDispatcher(DispatcherDeps{Engine: eng, Attachments: attachments.New(t.TempDir())})
	if err := eng.Dispatch(context.Background(), contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	return eng, disp
}

// A ref the store cannot resolve is an error, never a silent drop.
func TestImageInputsRefusesUnresolvableRefs(t *testing.T) {
	_, disp := imageDispatcher(t)
	for _, tc := range []struct{ name, thread, id string }{
		{"traversal thread id", "../escape", "0123456789abcdef"},
		{"traversal attachment id", "t1", "../../etc/passwd"},
		{"non-hex attachment id", "t1", "nothex"},
	} {
		if _, err := disp.imageInputs(contracts.ThreadID(tc.thread), []contracts.ImageRef{{ID: tc.id}}); err == nil {
			t.Errorf("%s: the unresolvable ref was silently dropped", tc.name)
		}
	}

	// A stored pair does resolve, carrying the recorded meta.
	ref, err := disp.attach.Save("t1", "bug.png", pngBytes(t))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	inputs, err := disp.imageInputs("t1", []contracts.ImageRef{ref})
	if err != nil {
		t.Fatalf("stored ref: %v", err)
	}
	if len(inputs) != 1 || inputs[0].Mime != ref.Mime || inputs[0].Name != ref.Name || inputs[0].Path == "" {
		t.Fatalf("inputs = %+v", inputs)
	}
}

// A turn whose images cannot be resolved to files is refused before the user
// message is recorded: the transcript must never promise an image the harness
// never sees.
func TestRunTurnRefusesUnresolvableImageRefBeforeRecording(t *testing.T) {
	eng, disp := imageDispatcher(t)

	cmd := contracts.Command{
		ID: "c2", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "look at this",
		Images: []contracts.ImageRef{{ID: "zz"}},
	}
	if _, err := disp.runTurn(context.Background(), "t1", "look at this", cmd); err == nil {
		t.Fatal("the turn ran although the image ref could not be resolved")
	}
	st, err := eng.State(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) != 0 {
		t.Fatalf("messages = %+v, want none: the refused turn was recorded", st.Messages)
	}
}
