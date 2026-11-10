// Package relay is atrelay: a self-hosted router that carries one daemon's
// JSON-RPC to its paired phones over outbound WebSockets. The wire contract is
// docs/relay-protocol.md. The relay never interprets RPC methods, persists no
// message bodies, and logs no frame contents.
package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
)

// Protocol limits and timers, from the protocol note.
const (
	Version           = 1
	MaxFrameBytes     = 4 << 20
	HelloTimeout      = 5 * time.Second
	PingInterval      = 30 * time.Second
	CodeTTL           = 5 * time.Minute
	TokenTTL          = 90 * 24 * time.Hour
	MaxFailedPerIP    = 5
	MaxFailedPerCode  = 20
	FailureWindow     = time.Minute
	OutboundQueue     = 256
	maxClientNameRune = 64
)

// WebSocket close codes for auth and rate limits.
const (
	CloseAuth        = 4401
	CloseRateLimited = 4429
)

// Error codes carried in ack `error` fields.
const (
	ErrBadHello           = "bad_hello"
	ErrBadCode            = "bad_code"
	ErrBadToken           = "bad_token"
	ErrBadSecret          = "bad_secret"
	ErrBadInstance        = "bad_instance"
	ErrRateLimited        = "rate_limited"
	ErrEncUnsupported     = "enc_unsupported"
	ErrUnsupportedVersion = "unsupported_version"
	ErrNotAttached        = "not_attached"
	errCodeDaemonOffline  = -32001
	errCodeDaemonBusy     = -32002
	msgDaemonOffline      = "daemon_offline"
	msgDaemonBusy         = "daemon_busy"
)

// Control frames. Each is relay-terminated and has a `t` field.
type (
	// Hello authenticates a socket. Daemons send Role, Instance, Secret, and an
	// optional Name label. Clients send Role and Token.
	Hello struct {
		T        string `json:"t"`
		V        int    `json:"v,omitempty"`
		Role     string `json:"role"`
		Instance string `json:"instance,omitempty"`
		Secret   string `json:"secret,omitempty"`
		Name     string `json:"name,omitempty"`
		Resume   string `json:"resume,omitempty"`
		Token    string `json:"token,omitempty"`
	}
	// Pair redeems a pairing code for a client token.
	Pair struct {
		T    string `json:"t"`
		Code string `json:"code"`
		Name string `json:"name,omitempty"`
	}
	// Attach binds an authenticated client socket to its instance.
	Attach struct {
		T        string `json:"t"`
		Instance string `json:"instance,omitempty"`
	}
	// Ack answers hello, pair, and attach.
	Ack struct {
		T            string `json:"t"`
		OK           bool   `json:"ok"`
		Error        string `json:"error,omitempty"`
		Instance     string `json:"instance,omitempty"`
		InstanceName string `json:"instanceName,omitempty"`
		Token        string `json:"token,omitempty"`
		ClientID     string `json:"clientId,omitempty"`
		Expires      string `json:"expires,omitempty"`
		Daemon       string `json:"daemon,omitempty"`
	}
	// Bye tells a peer the relay is closing its socket.
	Bye struct {
		T      string `json:"t"`
		Reason string `json:"reason"`
	}
	// Status reports daemon presence to a client. Resync asks it to re-read state.
	Status struct {
		T      string `json:"t"`
		Daemon string `json:"daemon,omitempty"`
		Resync bool   `json:"resync,omitempty"`
	}
	// PairOffer is the daemon's pairing code, sent when it asks for one.
	PairOffer struct {
		T    string `json:"t"`
		Code string `json:"code"`
	}
	// Revoke is a daemon's unpair request (ClientID, or All), or a phone
	// revoking its own token when both are empty.
	Revoke struct {
		T        string `json:"t"`
		ClientID string `json:"clientId,omitempty"`
		All      bool   `json:"all,omitempty"`
	}
	// Device is one paired phone, as the relay tells its daemon.
	Device struct {
		ClientID string `json:"clientId"`
		Name     string `json:"name"`
		PairedAt string `json:"pairedAt"`
	}
	// Devices is the relay's snapshot of an instance's paired phones, sent on
	// every daemon hello.
	Devices struct {
		T       string   `json:"t"`
		Devices []Device `json:"devices"`
	}
	// Paired and Unpaired keep the daemon's device list live. Detach tells the
	// daemon a phone's socket closed, so it can tear down that client's
	// subscriptions.
	Paired struct {
		T string `json:"t"`
		Device
	}
	Unpaired struct {
		T        string `json:"t"`
		ClientID string `json:"clientId"`
	}
	Detach struct {
		T        string `json:"t"`
		ClientID string `json:"clientId"`
	}
	ping struct {
		T string `json:"t"`
	}
	pong struct {
		T string `json:"t"`
	}
)

// Control frame type names.
const (
	tHello     = "hello"
	tPair      = "pair"
	tAttach    = "attach"
	tAck       = "ack"
	tPing      = "ping"
	tPong      = "pong"
	tBye       = "bye"
	tStatus    = "status"
	tPairOffer = "pair-offer"
	tRevoke    = "revoke"
	tDevices   = "devices"
	tPaired    = "paired"
	tUnpaired  = "unpaired"
	tDetach    = "detach"
)

// Reasons carried in bye frames.
const (
	reasonRevoked  = "revoked"
	reasonReplaced = "replaced"
	reasonShutdown = "shutdown"
)

// codeAlphabet is Crockford base32: no I, L, O, or U.
const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewCode mints a pairing code: 10 characters from a CSPRNG, grouped XXXXX-XXXXX.
// Each byte masks to 5 bits, and 32 divides 256, so every character is uniform.
func NewCode() (string, error) {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = codeAlphabet[v&31]
	}
	return string(out[:5]) + "-" + string(out[5:]), nil
}

// NormalizeCode canonicalizes typed or scanned input. Case, spaces, and dashes
// are ignored, and Crockford's confusable letters map to digits. It returns ""
// for anything that is not a valid code.
func NormalizeCode(in string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(in) {
		switch r {
		case ' ', '-', '\t':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		if !strings.ContainsRune(codeAlphabet, r) {
			return ""
		}
		sb.WriteRune(r)
	}
	s := sb.String()
	if len(s) != 10 {
		return ""
	}
	return s[:5] + "-" + s[5:]
}

// ValidateURL checks a relay base URL and returns it normalized. wss:// is
// always accepted. ws:// is accepted only for loopback or private addresses,
// and a URL may not carry a query or fragment, since no secret goes in a URL.
func ValidateURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", errors.New("relay URL must be wss:// or ws:// with a host")
	}
	switch u.Scheme {
	case "wss":
	case "ws":
		if !isLocalHost(u.Hostname()) {
			return "", errors.New("plain ws:// is allowed only for loopback or private hosts; use wss://")
		}
	default:
		return "", errors.New("relay URL must be wss:// or ws://")
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("relay URL must not carry credentials, a query, or a fragment")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String(), nil
}

func isLocalHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

// hashHex is the SHA-256 of s, hex encoded. Secrets, tokens, and codes are
// stored and compared only in this form.
func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// NewToken mints an opaque 256-bit client token, base64url encoded.
func NewToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// frame encodes a control frame. Control structs contain only marshalable
// fields, so a marshal error would be a programming bug.
func frame(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}
