package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/providers"
)

// pngBytes renders a small PNG for uploads.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{R: 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newImageDaemon runs every agent as a script that records each argv word on
// its own line, so a test can see both the prompt text and how images reached
// the harness. supported=false mirrors a harness whose invocation ignores
// spec.Images entirely (cursor, commandcode) and degrades to a prompt note.
func newImageDaemon(t *testing.T, captureFile string, supported bool) *Daemon {
	t.Helper()
	script := `: > '` + captureFile + `'
for arg in "$@"; do printf '%s\n' "$arg" >> '` + captureFile + `'; done
printf '{"type":"text","text":"ok"}\n'`
	driver := providers.NewGenericDriver(contracts.HarnessOpenCode, "sh", func(spec providers.SessionSpec) (providers.CommandSpec, error) {
		args := []string{"-c", script, "sh", spec.Prompt}
		if supported {
			for _, img := range spec.Images {
				args = append(args, "--file", img.Path)
			}
		}
		return providers.CommandSpec{Binary: "sh", Args: args, PromptVia: "arg"}, nil
	}, providers.JSONLineTranslator{})
	if supported {
		driver = driver.WithImageSupport(providers.ImagesSupported)
	}
	d, err := New(context.Background(), Options{
		StateDir: t.TempDir(),
		Token:    "secret",
		Registry: providers.NewRegistry(driver),
	})
	if err != nil {
		t.Fatalf("daemon: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

// uploadImage posts raw bytes to /attachments and decodes the ImageRef reply.
func uploadImage(t *testing.T, base string, threadID contracts.ThreadID, name string, data []byte) contracts.ImageRef {
	t.Helper()
	url := base + "/attachments?threadId=" + url.QueryEscape(string(threadID)) + "&name=" + url.QueryEscape(name)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload status = %d: %s", resp.StatusCode, body)
	}
	var ref contracts.ImageRef
	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func readCapture(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	return string(raw)
}

// The full image loop: upload over HTTP, start an image-bearing turn, read the
// meta back from thread.get, and fetch the bytes again by id. The WebSocket
// never carries image data in any direction.
func TestImageUploadTurnAndFetchBytes(t *testing.T) {
	dir := t.TempDir()
	captureFile := dir + "/capture.txt"
	d := newImageDaemon(t, captureFile, true)
	c, srvBase := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "t", "thread.create", map[string]any{"title": "images"}), &created)

	data := pngBytes(t, 4, 3)
	ref := uploadImage(t, srvBase, created.ThreadID, "shot of the bug.png", data)
	if ref.ID == "" || ref.Mime != "image/png" || ref.Size != int64(len(data)) {
		t.Fatalf("ref = %+v", ref)
	}
	if ref.Width != 4 || ref.Height != 3 || ref.Name != "shot of the bug.png" {
		t.Fatalf("ref = %+v", ref)
	}

	call(t, c, "turn", "thread.turn.start", map[string]any{
		"threadId": created.ThreadID,
		"text":     "what is in this picture?",
		"images":   []string{ref.ID},
	})

	// The harness saw the image: a supported lead gets it as an input file.
	captured := readCapture(t, captureFile)
	if !strings.Contains(captured, "--file") || !strings.Contains(captured, ref.ID) {
		t.Fatalf("harness never received the image arg:\n%s", captured)
	}
	if !strings.Contains(captured, "what is in this picture?") {
		t.Fatalf("prompt lost:\n%s", captured)
	}
	// A supported harness gets the real mechanism, not the degrade note.
	if strings.Contains(captured, "## Attached images") {
		t.Fatalf("supported harness got the degrade note:\n%s", captured)
	}

	// The transcript records meta, not bytes.
	var state struct {
		Messages []struct {
			Author string               `json:"author"`
			Text   string               `json:"text"`
			Images []contracts.ImageRef `json:"images"`
		} `json:"messages"`
	}
	json.Unmarshal(call(t, c, "g", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)
	if len(state.Messages) == 0 || len(state.Messages[0].Images) != 1 {
		t.Fatalf("messages = %+v", state.Messages)
	}
	got := state.Messages[0].Images[0]
	if got.ID != ref.ID || got.Mime != "image/png" || got.Width != 4 {
		t.Fatalf("transcript ref = %+v, want %+v", got, ref)
	}

	// The bytes come back over HTTP, with the stored type.
	req, _ := http.NewRequest(http.MethodGet, srvBase+"/attachments?threadId="+string(created.ThreadID)+"&id="+ref.ID, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("fetch status = %d type = %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, data) {
		t.Fatal("fetched bytes differ from the upload")
	}
}

// A harness that cannot take images natively still gets the turn: the images
// degrade to a prompt note listing name, mime and path, and the model decides.
func TestUnsupportedHarnessGetsImageNoteNotRejection(t *testing.T) {
	dir := t.TempDir()
	captureFile := dir + "/capture.txt"
	d := newImageDaemon(t, captureFile, false)
	c, srvBase := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "t", "thread.create", map[string]any{"title": "ungood"}), &created)

	ref := uploadImage(t, srvBase, created.ThreadID, "screen.png", pngBytes(t, 2, 2))
	call(t, c, "turn", "thread.turn.start", map[string]any{
		"threadId": created.ThreadID,
		"text":     "describe the screenshot",
		"images":   []string{ref.ID},
	})

	captured := readCapture(t, captureFile)
	if !strings.Contains(captured, "## Attached images") {
		t.Fatalf("degrade note missing:\n%s", captured)
	}
	if !strings.Contains(captured, "screen.png (image/png)") || !strings.Contains(captured, ref.ID) {
		t.Fatalf("note lacks name/mime/path:\n%s", captured)
	}
	if !strings.Contains(captured, "may not be able to view them") {
		t.Fatalf("note lacks the caveat:\n%s", captured)
	}
	// No image flags: the harness cannot take them.
	if strings.Contains(captured, "--file") {
		t.Fatalf("unsupported harness got image args:\n%s", captured)
	}
	// The turn itself was accepted and its agent ran.
	if !strings.Contains(captured, "describe the screenshot") {
		t.Fatalf("prompt lost:\n%s", captured)
	}
}

func TestAttachmentUploadsAreGatedAndValidated(t *testing.T) {
	// The capture file is only written when an agent runs; the refused turns
	// below must never spawn one, so its absence is the assertion.
	captureFile := t.TempDir() + "/capture.txt"
	d := newImageDaemon(t, captureFile, true)
	c, srvBase := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "t", "thread.create", map[string]any{"title": "gate"}), &created)

	// No token: refused.
	req, _ := http.NewRequest(http.MethodPost, srvBase+"/attachments?threadId="+string(created.ThreadID), strings.NewReader("x"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated upload status = %d", resp.StatusCode)
	}

	// Wrong bytes: refused with a plain message.
	req, _ = http.NewRequest(http.MethodPost, srvBase+"/attachments?threadId="+string(created.ThreadID)+"&name=a.txt", strings.NewReader("not an image"))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-image upload status = %d", resp.StatusCode)
	}

	// Unknown thread: refused.
	req, _ = http.NewRequest(http.MethodPost, srvBase+"/attachments?threadId=th_missing&name=a.png", bytes.NewReader(pngBytes(t, 1, 1)))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown-thread upload status = %d", resp.StatusCode)
	}

	// A turn naming an image id that was never uploaded is refused before
	// the agents run.
	if _, err := tryCall(t, c, "turn", "thread.turn.start", map[string]any{
		"threadId": created.ThreadID,
		"text":     "use the mystery image",
		"images":   []string{"0123456789abcdef0123456789abcdef"},
	}); err == nil {
		t.Fatal("turn with an unuploaded image id was accepted")
	}
	if _, err := os.Stat(captureFile); err == nil {
		t.Fatal("agent ran despite the rejected image")
	}
}

// Steering can carry images too: they land in the transcript with the
// steering message and reach the next pass like a turn's would.
func TestSteerWithImagesRecordsMeta(t *testing.T) {
	dir := t.TempDir()
	captureFile := dir + "/capture.txt"
	d := newImageDaemon(t, captureFile, true)
	c, srvBase := dialWS(t, d)

	var created struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	json.Unmarshal(call(t, c, "t", "thread.create", map[string]any{"title": "steer-img"}), &created)

	data := pngBytes(t, 2, 2)
	ref := uploadImage(t, srvBase, created.ThreadID, "later.png", data)
	call(t, c, "s", "thread.steer", map[string]any{
		"threadId": created.ThreadID,
		"text":     "also look at this",
		"images":   []string{ref.ID},
	})

	var state struct {
		Messages []struct {
			Author string               `json:"author"`
			Images []contracts.ImageRef `json:"images"`
		} `json:"messages"`
	}
	json.Unmarshal(call(t, c, "g", "thread.get", map[string]any{"threadId": created.ThreadID}), &state)
	if len(state.Messages) == 0 || state.Messages[0].Author != "steering" || len(state.Messages[0].Images) != 1 {
		t.Fatalf("messages = %+v", state.Messages)
	}
	if got := state.Messages[0].Images[0]; got.ID != ref.ID || got.Mime != "image/png" {
		t.Fatalf("steering ref = %+v", got)
	}

	// The queued item carries the image inputs for the next pass.
	items := d.Runner.DrainSteering(created.ThreadID)
	if len(items) != 1 || len(items[0].Images) != 1 || items[0].Images[0].Mime != "image/png" {
		t.Fatalf("queued steering = %+v", items)
	}
}

// harness.list reports image support per harness so clients can warn before
// attaching.
func TestHarnessListsImageSupport(t *testing.T) {
	d := newTestDaemon(t)
	c, _ := dialWS(t, d)
	var res struct {
		ImageSupport map[string]string `json:"imageSupport"`
	}
	json.Unmarshal(call(t, c, "h", "harness.list", nil), &res)
	if res.ImageSupport == nil {
		t.Fatal("harness.list has no imageSupport")
	}
	// The test daemon's driver declares nothing: unknown, which degrades
	// rather than refuses.
	if res.ImageSupport["opencode"] != "unknown" {
		t.Fatalf("imageSupport = %+v", res.ImageSupport)
	}
}
