package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// whatsappPushFrame is a remote.status.changed push carrying one WhatsApp item.
func whatsappPushFrame(t *testing.T, state, detail, qr string) Event {
	t.Helper()
	raw, err := json.Marshal(whatsappStatusFrame{Items: []whatsappItem{
		{ID: "phone", State: "off"},
		{ID: "whatsapp", State: state, Detail: detail, QR: qr},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return Event{Method: "remote.status.changed", Params: raw}
}

func TestWriteQRDrawsHalfBlocksInsideQuietZone(t *testing.T) {
	var buf bytes.Buffer
	if err := writeQR(&buf, "2@example-pairing-payload"); err != nil {
		t.Fatalf("writeQR: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) < 10 {
		t.Fatalf("QR has %d text lines, want a full code:\n%s", len(lines), buf.String())
	}
	width := len([]rune(lines[0]))
	for i, line := range lines {
		if got := len([]rune(line)); got != width {
			t.Fatalf("line %d is %d columns, want %d (ragged code):\n%s", i, got, width, buf.String())
		}
		for _, r := range line {
			if r != ' ' && r != '█' && r != '▀' && r != '▄' {
				t.Fatalf("line %d has non-block rune %q", i, r)
			}
		}
	}
	if strings.TrimSpace(lines[0]) != "" || strings.TrimSpace(lines[len(lines)-1]) != "" {
		t.Fatalf("the quiet zone must be blank on the top and bottom rows:\n%s", buf.String())
	}
}

func TestWriteQRIsDeterministicAndPayloadSensitive(t *testing.T) {
	render := func(payload string) string {
		var buf bytes.Buffer
		if err := writeQR(&buf, payload); err != nil {
			t.Fatalf("writeQR(%q): %v", payload, err)
		}
		return buf.String()
	}
	a := render("2@one")
	if a != render("2@one") {
		t.Fatal("the same payload must render the same code")
	}
	if a == render("2@two") {
		t.Fatal("different payloads rendered the same code")
	}
}

func TestPairingPaneRedrawsOverItsPreviousFrameOnTerminal(t *testing.T) {
	var buf bytes.Buffer
	pane := &pairingPane{out: &buf, tty: true}
	pane.show("Starting WhatsApp", "")
	first := pane.lines
	buf.Reset()

	pane.show("Scan with WhatsApp", "2@rotated")
	if want := "\x1b[" + strconv.Itoa(first) + "A\x1b[J"; !strings.HasPrefix(buf.String(), want) {
		t.Fatalf("terminal redraw must erase the previous %d lines first, got %q", first, buf.String()[:min(len(buf.String()), 30)])
	}

	buf.Reset()
	pane.show("Scan with WhatsApp", "2@rotated")
	if buf.Len() != 0 {
		t.Fatalf("an unchanged frame must not be redrawn, got %q", buf.String())
	}
}

func TestPairingPaneNeverEmitsCursorControlWhenPiped(t *testing.T) {
	var buf bytes.Buffer
	pane := newPairingPane(&buf)
	pane.show("Scan with WhatsApp", "2@a")
	pane.show("Scan with WhatsApp", "2@b")
	if strings.Contains(buf.String(), "\x1b[") {
		t.Fatalf("piped output must not contain ANSI cursor controls: %q", buf.String()[:min(len(buf.String()), 40)])
	}
}

func TestFollowWhatsAppPairingShowsQRThenLinks(t *testing.T) {
	events := make(chan Event, 2)
	events <- whatsappPushFrame(t, "pairing", "Scan the QR", "2@scan-me")
	events <- whatsappPushFrame(t, "connected", "Linked device · +15550000000", "")
	env, out := newUsageTestEnv()

	err := followWhatsAppPairing(context.Background(), env, events, &fakeCLICaller{}, whatsappItem{State: "connecting", Detail: "Starting WhatsApp"})
	if err != nil {
		t.Fatalf("followWhatsAppPairing: %v", err)
	}
	got := out.String()
	for _, want := range []string{"Starting WhatsApp", "Scan with WhatsApp > Linked devices", "Linked device · +15550000000", "█"} {
		if !strings.Contains(got, want) {
			t.Fatalf("pairing output missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "Scan the QR") > strings.Index(got, "Linked device") {
		t.Fatalf("the link must be reported after the QR was shown:\n%s", got)
	}
}

func TestFollowWhatsAppPairingReportsFailure(t *testing.T) {
	events := make(chan Event, 1)
	events <- whatsappPushFrame(t, "error", "the pairing QR expired; pair WhatsApp again", "")
	env, _ := newUsageTestEnv()

	err := followWhatsAppPairing(context.Background(), env, events, &fakeCLICaller{}, whatsappItem{State: "pairing", QR: "2@old"})
	if err == nil || !strings.Contains(err.Error(), "the pairing QR expired") {
		t.Fatalf("a failed pairing must surface its reason, got %v", err)
	}
}

func TestFollowWhatsAppPairingLeavingKeepsPairingRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env, out := newUsageTestEnv()

	err := followWhatsAppPairing(ctx, env, make(chan Event), &fakeCLICaller{}, whatsappItem{State: "pairing", Detail: "Scan", QR: "2@a"})
	if err != nil {
		t.Fatalf("leaving the watch is not a failure: %v", err)
	}
	if !strings.Contains(out.String(), "WhatsApp keeps pairing in the daemon") {
		t.Fatalf("leaving must say pairing continues:\n%s", out.String())
	}
}

func TestDecodeWhatsAppEventOnlyReadsTheWhatsAppRow(t *testing.T) {
	if _, ok := decodeWhatsAppEvent(Event{Method: "thread.event", Params: []byte(`{}`)}); ok {
		t.Fatal("non-status pushes must be ignored")
	}
	ev := whatsappPushFrame(t, "pairing", "Scan", "2@x")
	item, ok := decodeWhatsAppEvent(ev)
	if !ok || item.QR != "2@x" || item.State != "pairing" {
		t.Fatalf("decoded item = %+v, ok=%v", item, ok)
	}
}
