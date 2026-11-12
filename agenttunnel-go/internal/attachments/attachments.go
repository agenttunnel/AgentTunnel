// Package attachments stores image uploads on disk, one directory per
// thread. Bytes never ride the event log or the WebSocket: an upload answers
// with meta (id, mime, size, dimensions) and the bytes are fetched again over
// HTTP by id.
package attachments

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Errors returned by the store, mapped to plain-language client responses.
var (
	// ErrTooLarge is one image past contracts.MaxImageBytes.
	ErrTooLarge = fmt.Errorf("image exceeds the %d MiB limit", contracts.MaxImageBytes>>20)
	// ErrUnsupportedMime is a sniffed type outside the accepted set.
	ErrUnsupportedMime = errors.New("unsupported image type")
	// ErrNotFound is an unknown thread/id pair.
	ErrNotFound = errors.New("attachment not found")
	// ErrInvalidID is an id or thread id that cannot name a stored file.
	ErrInvalidID = errors.New("invalid attachment id")
)

// threadDirShape bounds a thread id used as a directory name: minted ids are
// "th_<hex>". Anything that could traverse a path is refused here before a
// filesystem operation ever runs.
var threadDirShape = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Store persists image attachments under a root directory, keyed by thread.
type Store struct {
	root string
}

// New returns a store rooted at dir; the directory is created on first save.
func New(dir string) *Store { return &Store{root: dir} }

// sidecar is the JSON meta stored beside an attachment's bytes.
type sidecar struct {
	Name       string    `json:"name"`
	Mime       string    `json:"mime"`
	Size       int64     `json:"size"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	UploadedAt time.Time `json:"uploadedAt"`
}

func (s sidecar) ref(id string) contracts.ImageRef {
	return contracts.ImageRef{ID: id, Name: s.Name, Mime: s.Mime, Size: s.Size, Width: s.Width, Height: s.Height}
}

// Save validates and stores data as a new attachment of thread, returning its
// meta. The mime is sniffed from the bytes themselves; the id is minted here.
func (st *Store) Save(threadID contracts.ThreadID, name string, data []byte) (contracts.ImageRef, error) {
	if !st.validThread(threadID) {
		return contracts.ImageRef{}, ErrInvalidID
	}
	if len(data) > contracts.MaxImageBytes {
		return contracts.ImageRef{}, ErrTooLarge
	}
	mime := http.DetectContentType(data)
	if !contracts.ValidImageMime(mime) {
		return contracts.ImageRef{}, fmt.Errorf("%w: %s", ErrUnsupportedMime, mime)
	}
	width, height := dimensions(data, mime)

	id, err := mintID()
	if err != nil {
		return contracts.ImageRef{}, err
	}
	dir := st.dir(threadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return contracts.ImageRef{}, err
	}
	meta := sidecar{Name: trimName(name), Mime: mime, Size: int64(len(data)), Width: width, Height: height, UploadedAt: time.Now().UTC()}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return contracts.ImageRef{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, id), data, 0o644); err != nil {
		return contracts.ImageRef{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), encoded, 0o644); err != nil {
		return contracts.ImageRef{}, err
	}
	return meta.ref(id), nil
}

// Open returns the stored meta and the referenced bytes.
func (st *Store) Open(threadID contracts.ThreadID, id string) (contracts.ImageRef, []byte, error) {
	meta, err := st.Meta(threadID, id)
	if err != nil {
		return contracts.ImageRef{}, nil, err
	}
	data, err := os.ReadFile(st.path(threadID, id))
	if err != nil {
		return contracts.ImageRef{}, nil, ErrNotFound
	}
	return meta, data, nil
}

// Meta returns the stored meta for one attachment, without the bytes.
func (st *Store) Meta(threadID contracts.ThreadID, id string) (contracts.ImageRef, error) {
	if !st.valid(threadID, id) {
		return contracts.ImageRef{}, ErrInvalidID
	}
	encoded, err := os.ReadFile(st.path(threadID, id) + ".json")
	if err != nil {
		return contracts.ImageRef{}, fmt.Errorf("%w: %s/%s", ErrNotFound, threadID, id)
	}
	var meta sidecar
	if err := json.Unmarshal(encoded, &meta); err != nil {
		return contracts.ImageRef{}, err
	}
	return meta.ref(id), nil
}

// Path returns the absolute path of the stored bytes, for a harness that
// reads the file itself. It returns "" when the pair is not valid.
func (st *Store) Path(threadID contracts.ThreadID, id string) string {
	if !st.valid(threadID, id) {
		return ""
	}
	return st.path(threadID, id)
}

// valid reports whether the pair can name a stored attachment.
func (st *Store) valid(threadID contracts.ThreadID, id string) bool {
	return st.validThread(threadID) && contracts.ValidImageID(id)
}

func (st *Store) validThread(threadID contracts.ThreadID) bool {
	return threadDirShape.MatchString(string(threadID))
}

// path joins the stored location; both components were shape-validated.
func (st *Store) path(threadID contracts.ThreadID, id string) string {
	return filepath.Join(st.dir(threadID), id)
}

func (st *Store) dir(threadID contracts.ThreadID) string {
	return filepath.Join(st.root, string(threadID))
}

// mintID returns a fresh attachment id: 16 random bytes, hex-encoded.
func mintID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// trimName keeps a client-supplied filename worth displaying but harmless.
func trimName(name string) string {
	name = filepath.Base(filepath.Clean("/" + name))
	if len(name) > 200 {
		name = name[len(name)-200:]
	}
	return name
}

// dimensions decodes an image's pixel size, falling back to 0,0 (unknown)
// rather than failing: the tag is metadata, never a gate.
func dimensions(data []byte, mime string) (int, int) {
	switch mime {
	case "image/png", "image/jpeg", "image/gif":
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return 0, 0
		}
		return cfg.Width, cfg.Height
	case "image/webp":
		return webpDimensions(data)
	}
	return 0, 0
}

// webpDimensions hand-parses the container header. The stdlib registers no
// webp decoder and the module takes no image dependency for one field.
func webpDimensions(b []byte) (int, int) {
	if len(b) < 16 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0
	}
	p := b[12:]
	for len(p) >= 8 {
		size := int(p[4]) | int(p[5])<<8 | int(p[6])<<16 | int(p[7])<<24
		body := p[8:]
		if len(body) < size {
			return 0, 0
		}
		switch string(p[:4]) {
		case "VP8X": // extended header: canvas size minus one, 24-bit LE
			if size >= 10 {
				w := int(body[4]) | int(body[5])<<8 | int(body[6])<<16
				h := int(body[7]) | int(body[8])<<8 | int(body[9])<<16
				return w + 1, h + 1
			}
		case "VP8 ": // lossy: start code then 14-bit LE width and height
			if size >= 10 && body[3] == 0x9d && body[4] == 0x01 && body[5] == 0x2a {
				w := int(body[6]) | int(body[7])<<8
				h := int(body[8]) | int(body[9])<<8
				return w & 0x3fff, h & 0x3fff
			}
		case "VP8L": // lossless: signature then 28 bits of (w-1, h-1)
			if size >= 5 && body[0] == 0x2f {
				bits := uint32(body[1]) | uint32(body[2])<<8 | uint32(body[3])<<16 | uint32(body[4])<<24
				return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1
			}
		}
		// Chunks are padded to even sizes.
		size += size & 1
		p = p[8+size:]
	}
	return 0, 0
}
