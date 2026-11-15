package attachments

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"

	_ "image/png"
)

// pngBytes renders a small PNG of the given size.
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

func TestSaveAndOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := New(dir)
	data := pngBytes(t, 3, 2)

	ref, err := st.Save("th_abc123", "shot of the bug.png", data)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if ref.Mime != "image/png" || ref.Size != int64(len(data)) || ref.Width != 3 || ref.Height != 2 {
		t.Fatalf("ref = %+v", ref)
	}
	if ref.Name != "shot of the bug.png" || !contracts.ValidImageID(ref.ID) {
		t.Fatalf("ref = %+v", ref)
	}

	// The bytes live outside the event store, in a per-thread directory.
	if _, _, err := st.Open("th_other", ref.ID); err == nil {
		t.Fatal("attachment leaked across threads")
	}

	meta, got, err := st.Open("th_abc123", ref.ID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if meta != ref {
		t.Fatalf("meta = %+v, want %+v", meta, ref)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("bytes differ from what was saved")
	}

	path := st.Path("th_abc123", ref.ID)
	if path == "" || !strings.HasPrefix(path, filepath.Join(dir, "th_abc123")) {
		t.Fatalf("path = %q", path)
	}
}

func TestSaveRejectsNonImagesAndOversized(t *testing.T) {
	st := New(t.TempDir())

	if _, err := st.Save("th_1", "note.txt", []byte("plainly not an image")); err == nil || !strings.Contains(err.Error(), "unsupported image type") {
		t.Fatalf("text save err = %v", err)
	}
	if _, err := st.Save("th_1", "big.png", bytes.Repeat([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 1024*1024+1)); err != ErrTooLarge {
		t.Fatalf("oversized save err = %v, want ErrTooLarge", err)
	}
}

func TestSaveConfinesThreadAndIDs(t *testing.T) {
	st := New(t.TempDir())
	if _, err := st.Save("../escape", "x.png", pngBytes(t, 1, 1)); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("traversal thread id err = %v, want ErrInvalidID", err)
	}
	if _, err := st.Save("th/child", "x.png", pngBytes(t, 1, 1)); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("path separator in thread id err = %v, want ErrInvalidID", err)
	}
	if _, _, err := st.Open("th_1", "../../etc/passwd"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("traversal attachment id err = %v, want ErrInvalidID", err)
	}
	if _, _, err := st.Open("th_1", "nothex"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("non-hex attachment id err = %v, want ErrInvalidID", err)
	}
	// Meta's own gates are pinned separately from Open's: a bad thread id must
	// be refused by validation, not merely fail to find a sidecar.
	if _, err := st.Meta("../escape", "0123456789abcdef"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("traversal thread id in Meta err = %v, want ErrInvalidID", err)
	}
	if _, err := st.Meta("th_1", "nothex"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("non-hex attachment id in Meta err = %v, want ErrInvalidID", err)
	}
	if p := st.Path("th_1", "zz"); p != "" {
		t.Fatalf("path for invalid id = %q, want empty", p)
	}
}

// A stored GIF decodes through the stdlib path.
func TestGIFDimensions(t *testing.T) {
	st := New(t.TempDir())
	var buf bytes.Buffer
	img := image.NewPaletted(image.Rect(0, 0, 5, 4), color.Palette{color.Black})
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	ref, err := st.Save("th_g", "anim.gif", buf.Bytes())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if ref.Mime != "image/gif" || ref.Width != 5 || ref.Height != 4 {
		t.Fatalf("ref = %+v", ref)
	}
}

// webp dimensions come from a hand-parse of the container header, so each
// variant is checked against bytes crafted the way the format specifies.
func TestWebpDimensions(t *testing.T) {
	chunk := func(tag string, body []byte) []byte {
		hdr := make([]byte, 8)
		copy(hdr, tag)
		binary.LittleEndian.PutUint32(hdr[4:], uint32(len(body)))
		return append(hdr, body...)
	}
	riff := func(chunks ...[]byte) []byte {
		var body []byte
		for _, c := range chunks {
			body = append(body, c...)
		}
		out := make([]byte, 12)
		copy(out, "RIFF")
		binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
		copy(out[8:], "WEBP")
		return append(out, body...)
	}

	cases := []struct {
		name string
		data []byte
		w, h int
	}{
		{"vp8x", riff(chunk("VP8X", append(make([]byte, 4), 9, 0, 0, 6, 0, 0))), 10, 7},
		{"vp8 lossy", riff(chunk("VP8 ", append(make([]byte, 3), 0x9d, 0x01, 0x2a, 0x03, 0x40, 0x02, 0x80))), 3, 2},
		{"vp8l", riff(chunk("VP8L", append([]byte{0x2f}, byte(0x02), byte(0x40), byte(0x00), byte(0x00)))), 3, 2},
		{"jumble before vp8x", riff(chunk("ICCP", []byte{1, 2, 3, 4}), chunk("VP8X", append(make([]byte, 4), 15, 0, 0, 15, 0, 0))), 16, 16},
		{"not webp", []byte("GIF89a whatever"), 0, 0},
		{"truncated", riff(chunk("VP8X", make([]byte, 4))), 0, 0},
	}
	for _, tc := range cases {
		w, h := webpDimensions(tc.data)
		if w != tc.w || h != tc.h {
			t.Errorf("%s: dimensions = %dx%d, want %dx%d", tc.name, w, h, tc.w, tc.h)
		}
	}
}

// A jpeg-upload decodes through the same stdlib path; a jpeg without a full
// frame records unknown dimensions rather than failing the upload.
func TestSaveAcceptsJPEG(t *testing.T) {
	st := New(t.TempDir())
	// Minimal JPEG SOF0 header: enough for sniffing and DecodeConfig.
	jpeg := []byte{
		0xff, 0xd8, // SOI
		// JFIF APP0: lets DecodeConfig answer from the SOF alone.
		0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
		0xff, 0xc0, 0x00, 0x0b, // SOF0 marker and segment length (counts itself)
		0x08,                   // precision
		0x00, 0x04, 0x00, 0x03, // height 4, width 3
		0x01,             // one component
		0x01, 0x22, 0x00, // component spec
		0xff, 0xd9, // EOI
	}
	if http.DetectContentType(jpeg) != "image/jpeg" {
		t.Fatalf("sniffed %q, want image/jpeg", http.DetectContentType(jpeg))
	}
	ref, err := st.Save("th_j", "photo.jpg", jpeg)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if ref.Mime != "image/jpeg" || ref.Width != 3 || ref.Height != 4 {
		t.Fatalf("ref = %+v", ref)
	}
}
