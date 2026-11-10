package relay

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Options configure a Server.
type Options struct {
	// StatePath is the JSON file instances and client tokens persist to. Empty
	// keeps everything in memory, which tests use. Message bodies never reach it.
	StatePath string
	// Now is the clock. Nil uses time.Now. Tests inject one to exercise expiry
	// without sleeping.
	Now func() time.Time
	// Logf receives connection events and error codes. It never receives frame
	// contents or secrets. Nil discards.
	Logf func(format string, args ...any)
}

// Server routes frames between daemons and their paired phones.
type Server struct {
	now  func() time.Time
	logf func(string, ...any)
	path string

	mu        sync.Mutex
	instances map[string]*instanceRecord // by instance id
	tokens    map[string]*tokenRecord    // by client id
	offers    map[string]*offer          // live pairing code, by instance id
	daemons   map[string]*link           // live daemon socket, by instance id
	clients   map[string]*link           // live client socket, by client id
	ipFails   map[string][]time.Time     // failed pair attempts, by source IP
}

// instanceRecord is a daemon the relay has seen. Only the secret's hash is kept.
type instanceRecord struct {
	ID         string `json:"id"`
	SecretHash string `json:"secretHash"`
	Name       string `json:"name"`
}

// tokenRecord is one paired phone. Only the token's hash is kept.
type tokenRecord struct {
	ClientID string    `json:"clientId"`
	Instance string    `json:"instance"`
	Name     string    `json:"name"`
	Hash     string    `json:"hash"`
	IssuedAt time.Time `json:"issuedAt"`
	Expires  time.Time `json:"expires"`
	LastSeen time.Time `json:"lastSeen"`
}

// offer is a live pairing code. It exists in memory only and is never written
// to the state file.
type offer struct {
	hash     string
	expires  time.Time
	failures int
}

type stateFile struct {
	Instances []*instanceRecord `json:"instances"`
	Tokens    []*tokenRecord    `json:"tokens"`
}

// New builds a Server, loading persisted instances and tokens from StatePath.
func New(opts Options) (*Server, error) {
	s := &Server{
		now:       time.Now,
		logf:      func(string, ...any) {},
		path:      opts.StatePath,
		instances: map[string]*instanceRecord{},
		tokens:    map[string]*tokenRecord{},
		offers:    map[string]*offer{},
		daemons:   map[string]*link{},
		clients:   map[string]*link{},
		ipFails:   map[string][]time.Time{},
	}
	if opts.Now != nil {
		s.now = opts.Now
	}
	if opts.Logf != nil {
		s.logf = opts.Logf
	}
	if s.path != "" {
		if err := s.load(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Handler serves /v1/daemon, /v1/client, and /healthz.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/v1/daemon", s.handleDaemon)
	mux.HandleFunc("/v1/client", s.handleClient)
	return mux
}

// Serve runs the relay on ln until ctx ends. Tests pass a listener on
// 127.0.0.1:0; production runs it behind a TLS terminator.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// --- daemon socket ---

func (s *Server) handleDaemon(w http.ResponseWriter, r *http.Request) {
	ws, err := accept(w, r)
	if err != nil {
		return
	}
	defer ws.CloseNow()

	data, err := readWithin(ws, HelloTimeout)
	if err != nil {
		return
	}
	var h Hello
	if err := json.Unmarshal(data, &h); err != nil || h.T != tHello || h.Role != "daemon" {
		reject(ws, ErrBadHello, CloseAuth)
		return
	}
	if h.V != 0 && h.V != Version {
		reject(ws, ErrUnsupportedVersion, CloseAuth)
		return
	}

	s.mu.Lock()
	inst, code := s.authDaemonLocked(h)
	if code != "" {
		s.mu.Unlock()
		s.logf("relay: daemon rejected: %s", code)
		reject(ws, code, CloseAuth)
		return
	}
	l := newLink(ws, inst.ID, "")
	old := s.daemons[inst.ID]
	s.daemons[inst.ID] = l
	devices := s.devicesLocked(inst.ID)
	watchers := s.clientsOfLocked(inst.ID)
	s.mu.Unlock()

	if old != nil {
		old.stop(frame(Bye{T: tBye, Reason: reasonReplaced}), websocket.StatusNormalClosure, reasonReplaced)
	}
	l.send(frame(Ack{T: tAck, OK: true, Instance: inst.ID, InstanceName: inst.Name}))
	l.send(frame(Devices{T: tDevices, Devices: devices}))
	s.logf("relay: daemon online")
	s.notifyDaemon(watchers, true)
	defer func() {
		s.detachDaemon(inst.ID, l)
		l.stop(nil, websocket.StatusNormalClosure, "")
	}()

	ctx := context.Background()
	for {
		data, err := readFrame(ctx, ws)
		if err != nil {
			return
		}
		s.routeDaemonFrame(l, data)
	}
}

// authDaemonLocked checks a daemon hello and registers the instance on first
// sight (trust on first use). Callers hold s.mu.
func (s *Server) authDaemonLocked(h Hello) (*instanceRecord, string) {
	if !validHex(h.Instance, 32) {
		return nil, ErrBadHello
	}
	if !validHex(h.Secret, 64) {
		return nil, ErrBadSecret
	}
	secretHash := hashHex(h.Secret)
	rec, ok := s.instances[h.Instance]
	if !ok {
		rec = &instanceRecord{ID: h.Instance, SecretHash: secretHash, Name: clampName(h.Name, "AgentTunnel")}
		s.instances[h.Instance] = rec
		s.persistLocked()
		return rec, ""
	}
	if subtle.ConstantTimeCompare([]byte(rec.SecretHash), []byte(secretHash)) != 1 {
		return nil, ErrBadSecret
	}
	return rec, ""
}

// routeDaemonFrame handles one frame from a daemon. A frame with `c` goes to
// that client; the rest are relay-terminated control frames.
func (s *Server) routeDaemonFrame(l *link, data []byte) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	if _, ok := m["enc"]; ok {
		l.send(frame(Ack{T: tAck, OK: false, Error: ErrEncUnsupported}))
		return
	}
	var t, c string
	_ = json.Unmarshal(m["t"], &t)
	_ = json.Unmarshal(m["c"], &c)

	if c != "" {
		s.forwardToClient(l.instance, c, data)
		return
	}
	switch t {
	case tPairOffer:
		var p PairOffer
		if json.Unmarshal(data, &p) == nil {
			s.setOffer(l.instance, p.Code)
		}
	case tRevoke:
		var r Revoke
		if json.Unmarshal(data, &r) == nil {
			s.revoke(l.instance, r.ClientID, r.All, false)
		}
	case tPing:
		l.send(frame(pong{T: tPong}))
	}
}

func (s *Server) setOffer(instance, code string) {
	norm := NormalizeCode(code)
	if norm == "" {
		s.logf("relay: daemon sent a malformed pairing code")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A new code supersedes the live one: at most one offer per instance.
	s.offers[instance] = &offer{hash: hashHex(norm), expires: s.now().Add(CodeTTL)}
}

// forwardToClient delivers one daemon frame to the client it names. A slow
// client that overflows its queue is disconnected; it re-reads on reconnect.
func (s *Server) forwardToClient(instance, clientID string, data []byte) {
	s.mu.Lock()
	cl := s.clients[clientID]
	s.mu.Unlock()
	if cl == nil || cl.instance != instance {
		return
	}
	if !cl.send(data) {
		s.logf("relay: client queue overflow; disconnecting")
		s.detachClient(cl)
		cl.stop(nil, websocket.StatusTryAgainLater, "overflow")
	}
}

func (s *Server) detachDaemon(instance string, l *link) {
	s.mu.Lock()
	if s.daemons[instance] != l {
		s.mu.Unlock()
		return
	}
	delete(s.daemons, instance)
	watchers := s.clientsOfLocked(instance)
	s.mu.Unlock()
	s.logf("relay: daemon offline")
	s.notifyDaemon(watchers, false)
}

// notifyDaemon pushes a daemon presence change to every attached client.
func (s *Server) notifyDaemon(clients []*link, online bool) {
	state := "offline"
	if online {
		state = "online"
	}
	data := frame(Status{T: tStatus, Daemon: state})
	for _, cl := range clients {
		cl.send(data)
	}
}

// --- client socket ---

func (s *Server) handleClient(w http.ResponseWriter, r *http.Request) {
	ws, err := accept(w, r)
	if err != nil {
		return
	}
	defer ws.CloseNow()

	data, err := readWithin(ws, HelloTimeout)
	if err != nil {
		return
	}
	var env struct {
		T string `json:"t"`
	}
	_ = json.Unmarshal(data, &env)
	switch env.T {
	case tPair:
		var p Pair
		if json.Unmarshal(data, &p) != nil {
			reject(ws, ErrBadCode, CloseAuth)
			return
		}
		s.servePair(ws, clientIP(r), p)
		return
	case tHello:
	default:
		reject(ws, ErrBadHello, CloseAuth)
		return
	}

	var h Hello
	if err := json.Unmarshal(data, &h); err != nil || h.Role != "client" {
		reject(ws, ErrBadHello, CloseAuth)
		return
	}
	if h.V != 0 && h.V != Version {
		reject(ws, ErrUnsupportedVersion, CloseAuth)
		return
	}
	s.mu.Lock()
	rec, code := s.authTokenLocked(h.Token)
	s.mu.Unlock()
	if code != "" {
		s.logf("relay: client rejected: %s", code)
		reject(ws, code, CloseAuth)
		return
	}
	writeFrame(ws, Ack{T: tAck, OK: true, ClientID: rec.ClientID})

	data, err = readWithin(ws, HelloTimeout)
	if err != nil {
		return
	}
	var at Attach
	if json.Unmarshal(data, &at) != nil || at.T != tAttach {
		reject(ws, ErrNotAttached, CloseAuth)
		return
	}
	if at.Instance != "" && at.Instance != rec.Instance {
		reject(ws, ErrBadInstance, CloseAuth)
		return
	}

	l := newLink(ws, rec.Instance, rec.ClientID)
	s.mu.Lock()
	old := s.clients[rec.ClientID]
	s.clients[rec.ClientID] = l
	daemon := "offline"
	if s.daemons[rec.Instance] != nil {
		daemon = "online"
	}
	s.mu.Unlock()
	if old != nil {
		old.stop(frame(Bye{T: tBye, Reason: reasonReplaced}), websocket.StatusNormalClosure, reasonReplaced)
	}
	l.send(frame(Ack{T: tAck, OK: true, ClientID: rec.ClientID, Daemon: daemon}))
	s.logf("relay: client attached")
	defer func() {
		s.detachClient(l)
		l.stop(nil, websocket.StatusNormalClosure, "")
	}()

	for {
		data, err := readFrame(context.Background(), ws)
		if err != nil {
			return
		}
		if !s.routeClientFrame(l, data) {
			return
		}
	}
}

// routeClientFrame handles one frame from an attached phone. It reports false
// when the socket should stop reading (the phone revoked itself).
func (s *Server) routeClientFrame(l *link, data []byte) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return true
	}
	if _, ok := m["enc"]; ok {
		l.send(frame(Ack{T: tAck, OK: false, Error: ErrEncUnsupported}))
		return true
	}
	var t string
	_ = json.Unmarshal(m["t"], &t)
	switch t {
	case "":
		s.routeDataToDaemon(l, m)
	case tPing:
		l.send(frame(pong{T: tPong}))
	case tRevoke:
		s.revoke(l.instance, l.clientID, false, true)
		return false
	}
	return true
}

// routeDataToDaemon tags a phone's JSON-RPC frame with its client id and sends
// it to the daemon. The relay reads only `id`, to answer when the daemon is
// unreachable. It never reads method or params, and never stores the frame.
func (s *Server) routeDataToDaemon(l *link, m map[string]json.RawMessage) {
	s.mu.Lock()
	d := s.daemons[l.instance]
	s.mu.Unlock()
	if d == nil {
		replyError(l, m["id"], errCodeDaemonOffline, msgDaemonOffline)
		return
	}
	cid, _ := json.Marshal(l.clientID)
	m["c"] = cid
	tagged, err := json.Marshal(m)
	if err != nil {
		return
	}
	if !d.send(tagged) {
		replyError(l, m["id"], errCodeDaemonBusy, msgDaemonBusy)
	}
}

func replyError(l *link, id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	resp := struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{ID: id}
	resp.Error.Code = code
	resp.Error.Message = msg
	l.send(frame(resp))
}

func (s *Server) detachClient(l *link) {
	s.mu.Lock()
	if s.clients[l.clientID] == l {
		delete(s.clients, l.clientID)
	}
	s.mu.Unlock()
}

// clientsOfLocked lists the attached clients of an instance. Callers hold s.mu.
func (s *Server) clientsOfLocked(instance string) []*link {
	var out []*link
	for _, cl := range s.clients {
		if cl.instance == instance {
			out = append(out, cl)
		}
	}
	return out
}

// devicesLocked lists an instance's paired phones. Callers hold s.mu.
func (s *Server) devicesLocked(instance string) []Device {
	out := []Device{}
	for _, rec := range s.tokens {
		if rec.Instance == instance {
			out = append(out, Device{ClientID: rec.ClientID, Name: rec.Name, PairedAt: rec.IssuedAt.UTC().Format(time.RFC3339)})
		}
	}
	return out
}

// --- pairing ---

// servePair redeems a pairing code. Every failure reads the same to the phone,
// so the response never reveals whether a code was wrong, expired, or used.
func (s *Server) servePair(ws *websocket.Conn, ip string, p Pair) {
	now := s.now()
	s.mu.Lock()
	if s.limitedLocked(ip, now) {
		s.mu.Unlock()
		s.logf("relay: pair rate limited")
		reject(ws, ErrRateLimited, CloseRateLimited)
		return
	}
	instanceID, ok := s.redeemLocked(p.Code, now)
	if !ok {
		s.recordFailureLocked(ip, now)
		s.mu.Unlock()
		s.logf("relay: pair rejected")
		reject(ws, ErrBadCode, CloseAuth)
		return
	}
	token, err := NewToken()
	if err != nil {
		s.mu.Unlock()
		reject(ws, ErrBadCode, CloseAuth)
		return
	}
	clientID := newClientID()
	rec := &tokenRecord{
		ClientID: clientID,
		Instance: instanceID,
		Name:     clampName(p.Name, "Phone"),
		Hash:     hashHex(token),
		IssuedAt: now,
		Expires:  now.Add(TokenTTL),
		LastSeen: now,
	}
	s.tokens[clientID] = rec
	s.persistLocked()
	inst := s.instances[instanceID]
	daemon := s.daemons[instanceID]
	s.mu.Unlock()

	if daemon != nil {
		daemon.send(frame(Paired{T: tPaired, Device: Device{ClientID: clientID, Name: rec.Name, PairedAt: now.UTC().Format(time.RFC3339)}}))
	}
	s.logf("relay: pair accepted")
	writeFrame(ws, Ack{
		T:            tAck,
		OK:           true,
		Instance:     instanceID,
		InstanceName: inst.Name,
		Token:        token,
		ClientID:     clientID,
		Expires:      rec.Expires.UTC().Format(time.RFC3339),
	})
	_ = ws.Close(websocket.StatusNormalClosure, "paired")
}

// redeemLocked consumes the live offer whose hash matches code. Expired offers
// are dropped on sight. Callers hold s.mu.
func (s *Server) redeemLocked(code string, now time.Time) (string, bool) {
	norm := NormalizeCode(code)
	if norm == "" {
		return "", false
	}
	want := []byte(hashHex(norm))
	matched := ""
	for instance, o := range s.offers {
		if !now.Before(o.expires) {
			delete(s.offers, instance)
			continue
		}
		// Compare every live offer, so timing does not reveal which one matched.
		if subtle.ConstantTimeCompare([]byte(o.hash), want) == 1 {
			matched = instance
		}
	}
	if matched == "" {
		return "", false
	}
	delete(s.offers, matched)
	return matched, true
}

// recordFailureLocked counts a failed pair attempt against its source IP and
// against every live code. The 20th failure invalidates the code. Callers hold s.mu.
func (s *Server) recordFailureLocked(ip string, now time.Time) {
	s.ipFails[ip] = append(pruneBefore(s.ipFails[ip], now.Add(-FailureWindow)), now)
	for instance, o := range s.offers {
		o.failures++
		if o.failures >= MaxFailedPerCode {
			delete(s.offers, instance)
		}
	}
}

// limitedLocked reports whether ip has used up its failed attempts this
// minute. Callers hold s.mu.
func (s *Server) limitedLocked(ip string, now time.Time) bool {
	s.ipFails[ip] = pruneBefore(s.ipFails[ip], now.Add(-FailureWindow))
	return len(s.ipFails[ip]) >= MaxFailedPerIP
}

func pruneBefore(times []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(times) && !times[i].After(cutoff) {
		i++
	}
	return times[i:]
}

// --- client auth and revoke ---

// authTokenLocked finds the token's record and slides its expiry. The loop
// visits every token without an early exit, so timing does not reveal a match.
// Callers hold s.mu.
func (s *Server) authTokenLocked(token string) (*tokenRecord, string) {
	if token == "" {
		return nil, ErrBadToken
	}
	want := []byte(hashHex(token))
	var found *tokenRecord
	for _, rec := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(rec.Hash), want) == 1 {
			found = rec
		}
	}
	now := s.now()
	if found == nil || !now.Before(found.Expires) {
		return nil, ErrBadToken
	}
	found.LastSeen = now
	found.Expires = now.Add(TokenTTL)
	s.persistLocked()
	return found, ""
}

// revoke deletes the named tokens for an instance (or all of them), closes any
// live client sockets for them, and tells the daemon when a phone revoked
// itself. Daemon-initiated revokes need no reply: the daemon already dropped
// the device locally.
func (s *Server) revoke(instance, clientID string, all, fromPhone bool) {
	s.mu.Lock()
	var removed []string
	var victims []*link
	for id, rec := range s.tokens {
		if rec.Instance != instance || (!all && id != clientID) {
			continue
		}
		delete(s.tokens, id)
		removed = append(removed, id)
		if l := s.clients[id]; l != nil {
			delete(s.clients, id)
			victims = append(victims, l)
		}
	}
	if len(removed) > 0 {
		s.persistLocked()
	}
	daemon := s.daemons[instance]
	s.mu.Unlock()

	for _, l := range victims {
		l.stop(frame(Bye{T: tBye, Reason: reasonRevoked}), websocket.StatusNormalClosure, reasonRevoked)
	}
	if fromPhone && daemon != nil {
		for _, id := range removed {
			daemon.send(frame(Unpaired{T: tUnpaired, ClientID: id}))
		}
	}
	if len(removed) > 0 {
		s.logf("relay: %d client(s) revoked", len(removed))
	}
}

// --- persistence ---

func (s *Server) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var st stateFile
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("relay state %s: %w", s.path, err)
	}
	for _, rec := range st.Instances {
		s.instances[rec.ID] = rec
	}
	for _, rec := range st.Tokens {
		s.tokens[rec.ClientID] = rec
	}
	return nil
}

// persistLocked rewrites the state file atomically. Only instance and token
// hashes are written. Callers hold s.mu.
func (s *Server) persistLocked() {
	if s.path == "" {
		return
	}
	st := stateFile{}
	for _, rec := range s.instances {
		st.Instances = append(st.Instances, rec)
	}
	for _, rec := range s.tokens {
		st.Tokens = append(st.Tokens, rec)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.logf("relay: persist: %v", err)
		return
	}
	tmp, err := os.CreateTemp(dir, "relay-*.tmp")
	if err != nil {
		s.logf("relay: persist: %v", err)
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		s.logf("relay: persist: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return
	}
	if err := os.Rename(name, s.path); err != nil {
		os.Remove(name)
		s.logf("relay: persist: %v", err)
	}
}

// --- helpers ---

func accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Native clients are not same-origin; auth is the hello frame, not Origin.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(MaxFrameBytes)
	return ws, nil
}

// readWithin reads one frame, closing the socket if none arrives in time.
func readWithin(ws *websocket.Conn, d time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return readFrame(ctx, ws)
}

func readFrame(ctx context.Context, ws *websocket.Conn) ([]byte, error) {
	_, data, err := ws.Read(ctx)
	return data, err
}

func writeFrame(ws *websocket.Conn, v any) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = ws.Write(ctx, websocket.MessageText, frame(v))
}

// reject answers a failed hello or pair with an ack error, then closes.
func reject(ws *websocket.Conn, code string, closeCode websocket.StatusCode) {
	writeFrame(ws, Ack{T: tAck, OK: false, Error: code})
	_ = ws.Close(closeCode, code)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func newClientID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func clampName(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return fallback
	}
	if r := []rune(name); len(r) > maxClientNameRune {
		name = string(r[:maxClientNameRune])
	}
	return name
}

// validHex reports whether s is exactly n hex characters.
func validHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
