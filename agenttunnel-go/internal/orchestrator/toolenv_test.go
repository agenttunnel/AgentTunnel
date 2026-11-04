package orchestrator

import (
	"strings"
	"testing"
)

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

// The tool only works if the agent can reach the daemon; this is the contract
// that regressed before (the desktop shell never set an endpoint).
func TestToolEnvCarriesEndpointAndToken(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	r.toolPath = "/opt/agenttunnel/agenttunnel"
	r.token = "secret"
	r.endpoint = func() string { return "http://127.0.0.1:54371" }

	got := envMap(r.toolEnv("t1", "a1"))
	want := map[string]string{
		"AT_THREAD_ID":  "t1",
		"AT_AGENT_ID":   "a1",
		"AT_TOOL":       "/opt/agenttunnel/agenttunnel",
		"AT_DAEMON_URL": "http://127.0.0.1:54371",
		"AT_TOKEN":      "secret",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestToolEnvOmitsEndpointWhenUnset(t *testing.T) {
	r, _, _ := newHarness(t, `printf '{"type":"text","text":"ok"}\n'`)
	if _, ok := envMap(r.toolEnv("t1", "a1"))["AT_DAEMON_URL"]; ok {
		t.Fatal("AT_DAEMON_URL must be omitted when no endpoint is configured")
	}
}
