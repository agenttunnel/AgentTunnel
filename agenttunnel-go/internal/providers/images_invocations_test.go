package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func imageSpec(paths ...string) SessionSpec {
	var imgs []ImageInput
	for _, p := range paths {
		imgs = append(imgs, ImageInput{Path: p, Mime: "image/png"})
	}
	return SessionSpec{Prompt: "describe this", Images: imgs}
}

func TestCodexInvocationAttachesImageFlags(t *testing.T) {
	cs, err := codexInvocation(imageSpec("/att/a.png", "/att/b.png"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"exec", "--dangerously-bypass-approvals-and-sandbox",
		"--image", "/att/a.png", "--image", "/att/b.png",
		"describe this",
	}
	if !reflect.DeepEqual(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
	if len(cs.StdinJSONFrames) != 0 {
		t.Fatalf("frames = %v, want none", cs.StdinJSONFrames)
	}
}

func TestOpenCodeInvocationAttachesFileFlags(t *testing.T) {
	cs, err := openCodeInvocation(imageSpec("/att/a.png"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--auto", "--format", "json", "--thinking", "--file", "/att/a.png", "describe this"}
	if !reflect.DeepEqual(cs.Args, want) {
		t.Fatalf("args = %v, want %v", cs.Args, want)
	}
}

func TestPiInvocationMentionsImagePaths(t *testing.T) {
	cs, err := piInvocation(imageSpec("/att/a.png", "/att/b.png"))
	if err != nil {
		t.Fatal(err)
	}
	// -p takes the whole question; the @mentions ride inside it.
	if !strings.Contains(cs.Args[3], "describe this @/att/a.png @/att/b.png") {
		t.Fatalf("prompt arg = %q", cs.Args[3])
	}
}

func TestClaudeInvocationSendsImagesViaStdinFrames(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "a.png")
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}
	if err := os.WriteFile(imgPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	cs, err := claudeInvocation(imageSpec(imgPath))
	if err != nil {
		t.Fatal(err)
	}
	wantTail := []string{"--dangerously-skip-permissions", "--input-format", "stream-json"}
	if got := cs.Args[len(cs.Args)-len(wantTail):]; !reflect.DeepEqual(got, wantTail) {
		t.Fatalf("args tail = %v, want %v (no positional prompt may follow)", got, wantTail)
	}
	// No prompt arg and no stdin prompt: the frames carry the whole message.
	for _, a := range cs.Args {
		if a == "describe this" {
			t.Fatalf("prompt leaked into argv: %v", cs.Args)
		}
	}
	if len(cs.StdinJSONFrames) != 1 {
		t.Fatalf("frames = %d, want 1", len(cs.StdinJSONFrames))
	}

	var frame struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source *struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(cs.StdinJSONFrames[0]), &frame); err != nil {
		t.Fatalf("frame is not JSON: %v\n%s", err, cs.StdinJSONFrames[0])
	}
	if frame.Type != "user" || frame.Message.Role != "user" {
		t.Fatalf("frame = %s/%s", frame.Type, frame.Message.Role)
	}
	blocks := frame.Message.Content
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want image+text", len(blocks))
	}
	if blocks[0].Type != "image" || blocks[0].Source == nil ||
		blocks[0].Source.MediaType != "image/png" || blocks[0].Source.Type != "base64" {
		t.Fatalf("image block = %+v", blocks[0])
	}
	if decoded, err := base64.StdEncoding.DecodeString(blocks[0].Source.Data); err != nil || !reflect.DeepEqual(decoded, data) {
		t.Fatalf("image data does not round-trip: %v", err)
	}
	if blocks[1].Type != "text" || blocks[1].Text != "describe this" {
		t.Fatalf("text block = %+v", blocks[1])
	}
}

func TestClaudeInvocationWithoutImagesKeepsPromptArg(t *testing.T) {
	cs, err := claudeInvocation(SessionSpec{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cs.Args[len(cs.Args)-1:], []string{"hi"}) {
		t.Fatalf("args = %v, want prompt positional", cs.Args)
	}
	if len(cs.StdinJSONFrames) != 0 {
		t.Fatalf("frames = %v", cs.StdinJSONFrames)
	}
}

func TestCursorAndCommandCodeIgnoreImages(t *testing.T) {
	for _, fn := range []func(SessionSpec) (CommandSpec, error){cursorInvocation, commandCodeInvocation} {
		cs, err := fn(imageSpec("/att/a.png"))
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range cs.Args {
			if a == "/att/a.png" {
				t.Fatalf("harness got an image path: %v", cs.Args)
			}
		}
		if len(cs.StdinJSONFrames) != 0 {
			t.Fatalf("frames = %v", cs.StdinJSONFrames)
		}
	}
}

func TestBuiltinImageSupportMatrix(t *testing.T) {
	r := NewRegistry(BuiltinDrivers()...)
	want := map[contracts.Harness]ImageSupport{
		contracts.HarnessOpenCode:    ImagesSupported,
		contracts.HarnessClaude:      ImagesSupported,
		contracts.HarnessCodex:       ImagesSupported,
		contracts.HarnessPi:          ImagesSupported,
		contracts.HarnessCursor:      ImagesUnsupported,
		contracts.HarnessCommandCode: ImagesUnsupported,
	}
	for h, expected := range want {
		if got := r.ImageSupport(h); got != expected {
			t.Errorf("%s image support = %v, want %v", h, got, expected)
		}
	}
	// An unregistered harness reads as unknown, not unsupported: both unknown
	// and unsupported degrade to the attached-images prompt note rather than
	// refusing the turn — runLeadPass appends the note whenever support is not
	// ImagesSupported (TestUnsupportedHarnessGetsImageNoteNotRejection pins
	// that) — and only a supported harness receives the files themselves.
	if got := r.ImageSupport("nope"); got != ImagesUnknown {
		t.Errorf("unknown harness support = %v", got)
	}
}

// A GenericDriver without an explicit capability reads as unknown, and the
// chainable setter is honored.
func TestGenericDriverImageSupportDefaultsAndOverrides(t *testing.T) {
	bare := NewGenericDriver(contracts.HarnessCodex, "codex", codexInvocation, JSONLineTranslator{})
	if bare.ImageSupport() != ImagesUnknown {
		t.Fatalf("bare driver support = %v", bare.ImageSupport())
	}
	if got := bare.WithImageSupport(ImagesUnsupported).ImageSupport(); got != ImagesUnsupported {
		t.Fatalf("chained support = %v", got)
	}
}

// The adapter must write stdin JSON frames to the child and close stdin
// right after, so a stream-json harness sees EOF and answers.
func TestProcessAdapterWritesStdinJSONFrames(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "img.png")
	if err := os.WriteFile(imgPath, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs, err := claudeInvocation(SessionSpec{Prompt: "hi", Images: []ImageInput{{Path: imgPath, Mime: "image/png"}}})
	if err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "captured.jsonl")
	stubDriver := NewGenericDriver(contracts.HarnessClaude, "sh", func(SessionSpec) (CommandSpec, error) {
		return CommandSpec{
			Binary: "sh",
			Args: []string{"-c", `cat > ` + capture + `
printf '{"type":"text","text":"frame seen"}\n'`},
			PromptVia:       "arg",
			StdinJSONFrames: cs.StdinJSONFrames,
		}, nil
	}, JSONLineTranslator{})

	a := NewProcessAdapter(stubDriver)
	if err := a.Start(context.Background(), SessionSpec{Prompt: "unused"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	events := collect(t, a)
	if len(events) == 0 || events[0].Kind != contracts.LogText || events[0].Text != "frame seen" {
		t.Fatalf("events = %+v", events)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != cs.StdinJSONFrames[0] {
		t.Fatalf("child saw %q, want the frame", string(raw))
	}
}
