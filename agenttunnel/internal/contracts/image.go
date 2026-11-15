package contracts

import "regexp"

// Image attachment caps. Enforced when an image is uploaded, and again by the
// decider when a message carries images, so an oversized turn is refused
// before any agent runs.
const (
	// MaxImageBytes bounds one uploaded image.
	MaxImageBytes = 8 << 20
	// MaxTurnImages bounds the images one turn may carry.
	MaxTurnImages = 8
	// MaxTurnImageBytes bounds the total image bytes one turn may carry.
	MaxTurnImageBytes = 32 << 20
)

// imageID matches a daemon-minted attachment id: lowercase hex, 16 to 64
// characters. Anything else cannot name a stored attachment.
var imageID = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// ValidImageID reports whether s is shaped like a minted attachment id, so it
// can never carry a path separator or traversal into the store.
func ValidImageID(s string) bool { return imageID.MatchString(s) }

// ValidImageMime reports whether m is an accepted image type: the formats each
// harness can ingest without conversion.
func ValidImageMime(m string) bool {
	switch m {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	}
	return false
}
