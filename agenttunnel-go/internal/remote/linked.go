package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	// Pure-Go SQLite driver: the release binaries cross-compile, so the
	// session store must not need cgo.
	_ "modernc.org/sqlite"
)

// LinkedAdapter pairs this daemon as a WhatsApp linked device. It surfaces the
// QR WhatsApp shows for a companion device, keeps the resulting session under
// StateDir so a restart reconnects without scanning again, and sends workflow
// updates to the user's own number.
type LinkedAdapter struct {
	stateDir string
	open     linkedOpener

	mu    sync.Mutex
	sess  linkedSession
	epoch int
}

// linkedSession is the WhatsApp web client the adapter drives. Keeping the
// surface this small lets tests replace the network client with a fake.
type linkedSession interface {
	// Connect dials WhatsApp. An unpaired session reports QR payloads until the
	// phone scans one; a paired session links at once. onLinked names the
	// device once messages can be sent, and onError reports a pairing that
	// failed after Connect returned (expired QR, unlinked from the phone, …).
	Connect(ctx context.Context, onQR func(string), onLinked func(string), onError func(error)) error
	Send(ctx context.Context, text string) error
	Logout(ctx context.Context) error
	Close()
}

// linkedOpener creates the session backed by stateDir. The whatsmeow
// implementation opens the SQLite device store; tests substitute a fake.
type linkedOpener func(ctx context.Context, stateDir string) (linkedSession, error)

// NewLinkedAdapter pairs a device whose session lives under stateDir.
func NewLinkedAdapter(stateDir string) *LinkedAdapter {
	return newLinkedAdapter(stateDir, openWhatsmeowSession)
}

func newLinkedAdapter(stateDir string, open linkedOpener) *LinkedAdapter {
	return &LinkedAdapter{stateDir: stateDir, open: open}
}

// Connect starts pairing. It returns once the session is dialing; QR payloads
// and the linked state then arrive through the callbacks. A session that fails
// after this returns is dropped, so onError can simply ask for a new attempt.
func (a *LinkedAdapter) Connect(ctx context.Context, onQR, onLinked func(string), onError func(error)) error {
	a.mu.Lock()
	if a.sess != nil {
		a.mu.Unlock()
		return nil
	}
	epoch := a.epoch
	a.mu.Unlock()

	sess, err := a.open(ctx, a.stateDir)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if a.sess != nil || a.epoch != epoch {
		// A concurrent Connect won, or a Disconnect landed while the store was
		// opening. Either way this session must not survive.
		a.mu.Unlock()
		sess.Close()
		return nil
	}
	a.sess = sess
	a.mu.Unlock()

	// The socket outlives the RPC that started it, so it must not inherit that
	// request's cancellation.
	onFail := func(err error) {
		a.clear(sess)
		if onError != nil {
			onError(err)
		}
	}
	if err := sess.Connect(context.Background(), onQR, onLinked, onFail); err != nil {
		a.clear(sess)
		return err
	}
	return nil
}

// Send delivers one text message to the linked account's own number.
func (a *LinkedAdapter) Send(ctx context.Context, text string) error {
	a.mu.Lock()
	sess := a.sess
	a.mu.Unlock()
	if sess == nil {
		return errors.New("whatsapp: no linked device")
	}
	return sess.Send(ctx, text)
}

// Disconnect unlinks the device: WhatsApp stops delivering to it, and the local
// session is deleted so the next connect pairs from a fresh QR.
func (a *LinkedAdapter) Disconnect() error {
	a.mu.Lock()
	a.epoch++
	sess := a.sess
	a.sess = nil
	a.mu.Unlock()
	if sess == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), logoutTimeout)
	defer cancel()
	err := sess.Logout(ctx)
	sess.Close()
	return err
}

// Close stops the socket without unlinking the device. The daemon calls it on
// shutdown; the saved session is what makes the next start reconnect silently.
func (a *LinkedAdapter) Close() {
	a.mu.Lock()
	a.epoch++
	sess := a.sess
	a.sess = nil
	a.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
}

// clear closes a session that failed to connect, so a retry opens a fresh one.
func (a *LinkedAdapter) clear(sess linkedSession) {
	a.mu.Lock()
	if a.sess == sess {
		a.sess = nil
	}
	a.mu.Unlock()
	sess.Close()
}

// whatsmeowSession is the linkedSession implementation backed by whatsmeow and
// an on-disk SQLite device store.
type whatsmeowSession struct {
	client    *whatsmeow.Client
	container *sqlstore.Container
	linked    sync.Once
	failed    sync.Once
	closed    sync.Once
}

// sqliteDSN builds the driver URI for the session store. It deliberately avoids
// net/url: a URL renders "file://state/whatsapp.db" for a relative directory and
// "file://C:%5CUsers…" for a Windows path, putting the first segment in the URI
// authority, which modernc.org/sqlite rejects. A plain "file:" prefix keeps the
// path opaque on every OS.
func sqliteDSN(dbPath string) string {
	return "file:" + strings.ReplaceAll(dbPath, "\\", "/") +
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
}

// openWhatsmeowSession opens (or creates) the device store in stateDir. A
// stored device reconnects without a QR; an empty store starts pairing.
func openWhatsmeowSession(ctx context.Context, stateDir string) (linkedSession, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("whatsapp: state directory: %w", err)
	}
	dbPath := filepath.Join(stateDir, "whatsapp.db")
	container, err := sqlstore.New(ctx, "sqlite", sqliteDSN(dbPath), nil)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: open session store: %w", err)
	}
	// The store holds the device's encryption keys; keep it as private as the
	// relay's identity file.
	if err := os.Chmod(dbPath, 0o600); err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("whatsapp: protect session store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("whatsapp: load session: %w", err)
	}
	return &whatsmeowSession{
		client:    whatsmeow.NewClient(device, nil),
		container: container,
	}, nil
}

func (s *whatsmeowSession) Connect(_ context.Context, onQR, onLinked func(string), onError func(error)) error {
	s.client.AddEventHandler(func(evt any) {
		switch evt.(type) {
		case *events.Connected:
			s.notifyLinked(onLinked)
		case *events.LoggedOut:
			// The phone removed this companion device; there is nothing left to
			// reconnect, so the bridge must stop claiming "connected".
			s.fail(onError, errors.New("the device was unlinked from the phone; pair it again"))
			s.client.Disconnect()
		case *events.StreamReplaced:
			// Another process authenticated with this session's keys.
			s.fail(onError, errors.New("another client replaced this session"))
			s.client.Disconnect()
		}
	})
	if s.client.Store.ID == nil {
		qr, err := s.client.GetQRChannel(context.Background())
		if err != nil {
			return fmt.Errorf("whatsapp: start QR pairing: %w", err)
		}
		go s.followQR(qr, onQR, onLinked, onError)
	}
	if err := s.client.Connect(); err != nil {
		return fmt.Errorf("whatsapp: connect: %w", err)
	}
	if s.client.Store.ID != nil {
		// A saved session authenticates in the background. Wait for it so a
		// reconnect reports connected (or a revoked device reports an error)
		// instead of looking stuck in "connecting".
		if !s.client.WaitForConnection(connectTimeout) {
			s.client.Disconnect()
			return errors.New("whatsapp: the linked device could not authenticate; pair it again")
		}
		s.notifyLinked(onLinked)
	}
	return nil
}

// followQR forwards pairing codes and turns every terminal QR event into an
// onError call, so an expired or failed pairing cannot leave the row lying in
// "pairing" with a dead code. After a failure it keeps draining the channel
// (whatsmeow's emitters block on sends) but forwards nothing more.
func (s *whatsmeowSession) followQR(qr <-chan whatsmeow.QRChannelItem, onQR, onLinked func(string), onError func(error)) {
	failed := false
	for item := range qr {
		if failed {
			continue
		}
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			if item.Code != "" {
				onQR(item.Code)
			}
		case whatsmeow.QRChannelSuccess.Event:
			// The device store is saved by now, so this is the moment the
			// link becomes usable.
			s.notifyLinked(onLinked)
		case whatsmeow.QRChannelScannedWithoutMultidevice.Event:
			// whatsmeow keeps the channel open (the same code can be rescanned),
			// but the bridge must leave "pairing" and say what to fix.
			failed = true
			s.fail(onError, errors.New("that phone has multi-device disabled; enable it in WhatsApp and pair again"))
		case whatsmeow.QRChannelTimeout.Event:
			failed = true
			s.fail(onError, errors.New("the pairing QR expired; pair WhatsApp again"))
			s.client.Disconnect()
		case whatsmeow.QRChannelClientOutdated.Event:
			failed = true
			s.fail(onError, errors.New("this WhatsApp client is out of date; update AgentTunnel and pair again"))
			s.client.Disconnect()
		case whatsmeow.QRChannelErrUnexpectedEvent.Event:
			failed = true
			s.fail(onError, errors.New("WhatsApp ended pairing unexpectedly; pair again"))
			s.client.Disconnect()
		case whatsmeow.QRChannelEventError:
			failed = true
			reason := item.Error
			if reason == nil {
				reason = errors.New("unknown error")
			}
			s.fail(onError, fmt.Errorf("WhatsApp pairing failed: %w", reason))
			s.client.Disconnect()
		}
	}
}

func (s *whatsmeowSession) notifyLinked(onLinked func(string)) {
	s.linked.Do(func() {
		if onLinked != nil {
			onLinked(s.detail())
		}
	})
}

// fail reports the first terminal pairing/connection failure only. Later events
// (whatsmeow may emit two on a revoked device) must not overwrite the message
// the user is reading.
func (s *whatsmeowSession) fail(onError func(error), err error) {
	s.failed.Do(func() {
		if onError != nil {
			onError(err)
		}
	})
}

// detail is the row text once the device is linked.
func (s *whatsmeowSession) detail() string {
	if jid := s.client.Store.GetJID(); jid.User != "" {
		return "Linked device · +" + jid.User
	}
	return "WhatsApp linked device"
}

// Send messages the user's own number, which is where a companion device sees
// the conversation with itself.
func (s *whatsmeowSession) Send(ctx context.Context, text string) error {
	jid := s.client.Store.GetJID()
	if jid.IsEmpty() {
		return whatsmeow.ErrNotLoggedIn
	}
	_, err := s.client.SendMessage(ctx, jid.ToNonAD(), &waE2E.Message{
		Conversation: proto.String(text),
	})
	return err
}

// Logout unlinks the companion device. An unpaired session has nothing to
// unlink, so closing it is enough.
func (s *whatsmeowSession) Logout(ctx context.Context) error {
	if s.client.Store.ID == nil {
		s.client.Disconnect()
		return nil
	}
	return s.client.Logout(ctx)
}

func (s *whatsmeowSession) Close() {
	s.closed.Do(func() {
		s.client.Disconnect()
		if s.container != nil {
			_ = s.container.Close()
		}
	})
}

// logoutTimeout bounds the unlink request so a dead network cannot wedge the
// Disconnect RPC.
const logoutTimeout = 15 * time.Second

// connectTimeout bounds the wait for a saved session to authenticate.
const connectTimeout = 20 * time.Second
