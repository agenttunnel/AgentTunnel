package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/discovery"
)

// testLeaf is a command whose output lets a test see exactly what it received.
func registerTestCommand(t *testing.T, name string) {
	t.Helper()
	Register(Command{
		Name: name, Summary: "test command", Usage: "agenttunnel " + name,
		Run: func(_ context.Context, env *Env, args []string) error {
			if env.JSON {
				return env.PrintJSON(map[string]any{"args": args, "json": true})
			}
			env.Out.Write([]byte("args=" + strings.Join(args, ",") + "\n"))
			return nil
		},
	})
}

func TestGlobalFlagsAreParsedAnywhere(t *testing.T) {
	registerTestCommand(t, "test-flags")
	var out, errOut bytes.Buffer
	code := Run([]string{"test-flags", "a", "--json", "--home-dir", "/tmp/state", "b"}, "9.9.9", &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut.String())
	}
	var got struct {
		Args []string `json:"args"`
		JSON bool     `json:"json"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json: %v (%q)", err, out.String())
	}
	if !got.JSON || !reflect.DeepEqual(got.Args, []string{"a", "b"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"definitely-not-a-command"}, "dev", &out, &errOut)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestGroupDispatchesToSubcommand(t *testing.T) {
	Register(Command{
		Name: "test-group", Summary: "group",
		Subcommands: []Command{{Name: "sub", Summary: "sub", Usage: "agenttunnel test-group sub",
			Run: func(_ context.Context, env *Env, args []string) error {
				env.Out.Write([]byte("sub:" + strings.Join(args, ",") + "\n"))
				return nil
			}}},
	})
	var out, errOut bytes.Buffer
	if code := Run([]string{"test-group", "sub", "x"}, "dev", &out, &errOut); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut.String())
	}
	if got := strings.TrimSpace(out.String()); got != "sub:x" {
		t.Fatalf("out = %q", got)
	}

	// A group with no subcommand is a usage error, and prints the group help.
	out.Reset()
	if code := Run([]string{"test-group"}, "dev", &out, &errOut); code != 2 {
		t.Fatalf("bare group code = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "sub") {
		t.Fatalf("group help = %q", out.String())
	}

	// An unknown subcommand is a usage error too.
	if code := Run([]string{"test-group", "nope"}, "dev", &out, &errOut); code != 2 {
		t.Fatalf("unknown sub code = %d, want 2", code)
	}
}

func TestHelpListsRegisteredCommands(t *testing.T) {
	registerTestCommand(t, "test-listed")
	var out, errOut bytes.Buffer
	if code := Run([]string{"--help"}, "dev", &out, &errOut); code != 0 {
		t.Fatalf("code = %d", code)
	}
	help := out.String()
	for _, want := range []string{"test-listed", "daemon", "version", "--json", "--daemon", "--home-dir"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"version", "--json"}, "1.2.3", &out, &errOut); code != 0 {
		t.Fatalf("code = %d", code)
	}
	var got map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["version"] != "1.2.3" {
		t.Fatalf("out = %q (%v)", out.String(), err)
	}

	out.Reset()
	if code := Run([]string{"--version"}, "1.2.3", &out, &errOut); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.TrimSpace(out.String()) != "agenttunnel 1.2.3" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestNoArgsIsUsageError(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run(nil, "dev", &out, &errOut); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "Usage: agenttunnel") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestParseGlobalErrors(t *testing.T) {
	env := &Env{}
	if _, _, err := parseGlobal([]string{"--daemon"}, env); err == nil {
		t.Fatal("--daemon without a value should fail")
	}
	if _, _, err := parseGlobal([]string{"--home-dir"}, env); err == nil {
		t.Fatal("--home-dir without a value should fail")
	}
	env = &Env{}
	rest, help, err := parseGlobal([]string{"--json", "--", "--not-a-flag"}, env)
	if err != nil || help || !env.JSON || !reflect.DeepEqual(rest, []string{"--not-a-flag"}) {
		t.Fatalf("rest=%v help=%v json=%v err=%v", rest, help, env.JSON, err)
	}
}

func TestScrubEnvRemovesAgentSessionVariables(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"AT_AGENT_ID=agent-executor",
		"AT_THREAD_ID=th_1",
		"AT_DAEMON_URL=ws://127.0.0.1:1/ws",
		"AT_TOKEN=secret",
		"AT_TOOL=/tmp/tool",
		"HOME=/home/x",
		// A prefix match must not drop unrelated variables.
		"AT_AGENT_IDENTITY=keep",
	}
	got := scrubEnv(env)
	want := []string{"PATH=/usr/bin", "HOME=/home/x", "AT_AGENT_IDENTITY=keep"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scrubEnv = %v, want %v", got, want)
	}
}

// The autostart refusal is checked before any process is spawned.
func TestConnectRefusesAutostartInsideAgentSession(t *testing.T) {
	t.Setenv("AT_AGENT_ID", "agent-x")
	env := &Env{HomeDir: t.TempDir()}
	_, _, err := env.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Refusing to start a nested daemon") {
		t.Fatalf("err = %v", err)
	}
	// It must not have written a discovery file, either.
	if _, statErr := os.Stat(discovery.Path(env.HomeDir)); !os.IsNotExist(statErr) {
		t.Fatalf("autostart wrote %s despite the refusal", discovery.Path(env.HomeDir))
	}
}
