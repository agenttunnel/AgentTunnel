package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"rsc.io/qr"
)

func init() {
	Register(Command{
		Name:    "whatsapp",
		Summary: "Link WhatsApp to the daemon for workflow updates",
		Subcommands: []Command{
			{
				Name:    "connect",
				Summary: "Pair WhatsApp by scanning a QR code in the terminal",
				Usage:   "agenttunnel whatsapp connect",
				Run:     runWhatsAppConnect,
			},
		},
	})
}

// whatsappStatusFrame is the part of the remote status payload the CLI reads,
// both as the remote.whatsapp.connect reply and as remote.status.changed pushes.
type whatsappStatusFrame struct {
	Items []whatsappItem `json:"items"`
}

type whatsappItem struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	QR     string `json:"qr"`
}

// whatsappResync bounds how stale the pairing view can get. Status pushes are
// hints that a slow client may miss, so the CLI also re-reads the status.
const whatsappResync = 2 * time.Second

func runWhatsAppConnect(ctx context.Context, env *Env, args []string) error {
	if len(args) > 0 {
		return usageError("agenttunnel whatsapp connect takes no arguments")
	}
	c, _, err := env.Connect(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	ctx, stop := withInterrupt(ctx)
	defer stop()

	var reply whatsappStatusFrame
	if err := c.Call(ctx, "remote.whatsapp.connect", nil, &reply); err != nil {
		if ctx.Err() != nil {
			return detachOnCancel(ctx, env, nil)
		}
		return err
	}
	item, ok := whatsappItemOf(reply)
	if !ok {
		return errors.New("the daemon did not report a WhatsApp item")
	}
	return followWhatsAppPairing(ctx, env, c.Events(), c, item)
}

// followWhatsAppPairing shows the pairing until the device links, pairing fails,
// or the user leaves. Leaving does not unlink: the daemon keeps pairing, and the
// next `whatsapp connect` shows the current code again.
func followWhatsAppPairing(ctx context.Context, env *Env, events <-chan Event, c cliCaller, item whatsappItem) error {
	pane := newPairingPane(env.Out)
	resync := time.NewTicker(whatsappResync)
	defer resync.Stop()
	for {
		pane.show(item.Detail, item.QR)
		switch item.State {
		case "connected":
			return nil
		case "error":
			return fmt.Errorf("whatsapp: %s", item.Detail)
		case "off":
			return errors.New("whatsapp: pairing was cancelled")
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(env.Out, "\nstopped watching (WhatsApp keeps pairing in the daemon)")
			return nil
		case ev, ok := <-events:
			if !ok {
				return errors.New("lost the connection to the daemon")
			}
			if next, found := decodeWhatsAppEvent(ev); found {
				item = next
			}
		case <-resync.C:
			var status whatsappStatusFrame
			if err := c.Call(ctx, "remote.status", nil, &status); err != nil {
				if ctx.Err() != nil {
					continue
				}
				return err
			}
			if next, found := whatsappItemOf(status); found {
				item = next
			}
		}
	}
}

// decodeWhatsAppEvent reads a remote.status.changed push for the WhatsApp row.
func decodeWhatsAppEvent(ev Event) (whatsappItem, bool) {
	if ev.Method != "remote.status.changed" {
		return whatsappItem{}, false
	}
	var frame whatsappStatusFrame
	if err := json.Unmarshal(ev.Params, &frame); err != nil {
		return whatsappItem{}, false
	}
	return whatsappItemOf(frame)
}

func whatsappItemOf(frame whatsappStatusFrame) (whatsappItem, bool) {
	for _, item := range frame.Items {
		if item.ID == "whatsapp" {
			return item, true
		}
	}
	return whatsappItem{}, false
}

// pairingPane draws the pairing status and QR. On a terminal it redraws over the
// previous frame, so a rotated code replaces the old one instead of stacking.
type pairingPane struct {
	out   io.Writer
	tty   bool
	lines int
	last  string
}

func newPairingPane(out io.Writer) *pairingPane {
	return &pairingPane{out: out, tty: isTerminalWriter(out)}
}

// show draws the status line and, while one is waiting to be scanned, the QR.
// An unchanged frame is not redrawn.
func (p *pairingPane) show(detail, qrPayload string) {
	frame := detail + "\x00" + qrPayload
	if frame == p.last {
		return
	}
	p.last = frame

	var buf bytes.Buffer
	fmt.Fprintln(&buf, detail)
	if qrPayload != "" {
		fmt.Fprintln(&buf, "Scan with WhatsApp > Linked devices > Link a device")
		if err := writeQR(&buf, qrPayload); err != nil {
			fmt.Fprintln(&buf, "could not draw the QR code:", err)
		}
	}
	if p.tty && p.lines > 0 {
		fmt.Fprintf(p.out, "\x1b[%dA\x1b[J", p.lines)
	}
	p.out.Write(buf.Bytes())
	p.lines = strings.Count(buf.String(), "\n")
}

// qrQuietZone is the blank margin, in modules, that phone scanners need around
// the code.
const qrQuietZone = 4

// writeQR draws payload as a terminal QR code: each text line carries two module
// rows as half-block characters, so the code is roughly square on screen.
func writeQR(w io.Writer, payload string) error {
	code, err := qr.Encode(payload, qr.M)
	if err != nil {
		return fmt.Errorf("encode QR: %w", err)
	}
	dark := func(x, y int) bool { return code.Black(x-qrQuietZone, y-qrQuietZone) }
	span := code.Size + 2*qrQuietZone
	var line strings.Builder
	for y := 0; y < span; y += 2 {
		line.Reset()
		for x := 0; x < span; x++ {
			top, bottom := dark(x, y), dark(x, y+1)
			switch {
			case top && bottom:
				line.WriteString("█")
			case top:
				line.WriteString("▀")
			case bottom:
				line.WriteString("▄")
			default:
				line.WriteString(" ")
			}
		}
		fmt.Fprintln(w, line.String())
	}
	return nil
}

// isTerminalWriter reports whether w is a character device, i.e. a terminal
// rather than a pipe or file, so ANSI cursor movement is safe to emit.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
