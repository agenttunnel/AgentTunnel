package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// goldenSpec is fixed so the golden files do not depend on the machine.
var goldenSpec = serviceSpec{
	Binary:   "/Applications/AgentTunnel/atd",
	StateDir: "/Users/test/.agenttunnel",
	LogFile:  "/Users/test/.agenttunnel/atd.log",
}

// fakeRunner records every service-manager command instead of running it.
// reply, when set, chooses the output and error for each argv.
type fakeRunner struct {
	calls [][]string
	reply func(argv []string) (string, error)
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, error) {
	argv := append([]string{name}, args...)
	f.calls = append(f.calls, argv)
	if f.reply != nil {
		return f.reply(argv)
	}
	return "", nil
}

// checkGolden compares got with testdata/service/<name>. Set UPDATE_GOLDEN=1 to
// rewrite the file after an intended renderer change.
func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "service", name)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from golden:\n%s", name, got)
	}
}

func TestLaunchAgentGolden(t *testing.T) {
	checkGolden(t, "launchd.plist.golden", renderLaunchAgent(goldenSpec))
}

func TestSystemdUnitGolden(t *testing.T) {
	checkGolden(t, "systemd.service.golden", renderSystemdUnit(goldenSpec))
}

func TestTaskXMLGolden(t *testing.T) {
	checkGolden(t, "task.xml.golden", renderTaskXML(goldenSpec))
}

// Every renderer runs atd with the same argv and never carries an AT_* variable:
// the service must not inherit the installing shell's agent session.
func TestRenderedUnitsRunAtdWithoutAgentEnvironment(t *testing.T) {
	for name, out := range map[string]string{
		"launchd": renderLaunchAgent(goldenSpec),
		"systemd": renderSystemdUnit(goldenSpec),
		"task":    renderTaskXML(goldenSpec),
	} {
		if strings.Contains(out, "AT_") {
			t.Errorf("%s renders an AT_* variable:\n%s", name, out)
		}
		for _, want := range []string{"--port", "0", "--home-dir", "/Users/test/.agenttunnel", "--log-file", "atd.log"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s does not carry %q", name, want)
			}
		}
	}
}

func TestLaunchAgentRestartPolicy(t *testing.T) {
	out := renderLaunchAgent(goldenSpec)
	for _, want := range []string{
		"<string>" + serviceLabel + "</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>SuccessfulExit</key>\n\t\t<false/>",
		"<key>ThrottleInterval</key>\n\t<integer>10</integer>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

func TestSystemdRestartPolicy(t *testing.T) {
	out := renderSystemdUnit(goldenSpec)
	for _, want := range []string{"Restart=on-failure", "RestartSec=5", "StartLimitBurst=5", "StartLimitIntervalSec=300", "WantedBy=default.target"} {
		if !strings.Contains(out, want) {
			t.Errorf("unit missing %q", want)
		}
	}
}

func TestSystemdQuoteEscapesSpecifiers(t *testing.T) {
	if got := systemdQuote(`/a b/%c"d\e`); got != `"/a b/%%c\"d\\e"` {
		t.Fatalf("systemdQuote = %s", got)
	}
}

func TestWinArgQuotesOnlyWhenNeeded(t *testing.T) {
	if got := winArg("--port"); got != "--port" {
		t.Fatalf("plain word quoted: %s", got)
	}
	if got := winArg(`C:\Users\me\state dir`); got != `"C:\Users\me\state dir"` {
		t.Fatalf("spaced path = %s", got)
	}
}

func TestTaskXMLIsUTF16Encodable(t *testing.T) {
	raw := encodeUTF16(renderTaskXML(goldenSpec))
	if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xFE {
		t.Fatalf("missing UTF-16LE BOM: % x", raw[:2])
	}
	units := make([]uint16, 0, (len(raw)-2)/2)
	for i := 2; i+1 < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	if got := string(utf16.Decode(units)); got != renderTaskXML(goldenSpec) {
		t.Fatal("UTF-16 round trip changed the XML")
	}
}

func TestDaemonServiceCommandsRefuseAgentSession(t *testing.T) {
	t.Setenv("AT_AGENT_ID", "agent-x")
	fake := &fakeRunner{}
	prev := serviceCommands
	serviceCommands = fake
	t.Cleanup(func() { serviceCommands = prev })

	var out bytes.Buffer
	env := &Env{Out: &out, Err: &out, HomeDir: t.TempDir()}
	for name, run := range map[string]func(context.Context, *Env, []string) error{
		"install":   runDaemonInstall,
		"uninstall": runDaemonUninstall,
	} {
		err := run(context.Background(), env, nil)
		if err == nil || !strings.Contains(err.Error(), "AT_AGENT_ID") {
			t.Fatalf("%s: err = %v, want the AT_AGENT_ID refusal", name, err)
		}
	}
	if len(fake.calls) != 0 {
		t.Fatalf("refused commands ran service tools: %v", fake.calls)
	}
}

func TestServiceStatusJSONShape(t *testing.T) {
	raw, err := json.Marshal(serviceStatus{Installed: true, Loaded: true, Running: false, Service: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"installed", "loaded", "running", "service"} {
		if _, ok := got[key]; !ok {
			t.Errorf("status JSON missing %q: %s", key, raw)
		}
	}
}

// With no daemon.json the status is "not running" and never connects, so the
// wrapper only adds the service object. Both output modes carry it.
func TestDaemonStatusReportsServiceBesideDaemon(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	prev := serviceCommands
	serviceCommands = &fakeRunner{}
	t.Cleanup(func() { serviceCommands = prev })

	var out bytes.Buffer
	env := &Env{JSON: true, Out: &out, Err: &out, HomeDir: t.TempDir()}
	if code := exitCode(env, runDaemonStatusService(context.Background(), env, nil)); code != 1 {
		t.Fatalf("exit = %d, want 1 (not running)", code)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("status JSON: %v\n%s", err, out.String())
	}
	if doc["running"] != false {
		t.Fatalf("running = %v, want false", doc["running"])
	}
	if _, ok := doc["service"].(map[string]any); !ok {
		t.Fatalf("service object missing: %s", out.String())
	}

	out.Reset()
	env = &Env{Out: &out, Err: &out, HomeDir: t.TempDir()}
	_ = runDaemonStatusService(context.Background(), env, nil)
	if !strings.Contains(out.String(), "daemon is not running") || !strings.Contains(out.String(), "service: installed=") {
		t.Fatalf("text status = %q", out.String())
	}
}
