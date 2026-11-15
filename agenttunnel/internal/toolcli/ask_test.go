package toolcli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// askStub answers `tool ask` with a fixed daemon reply and records what the CLI
// posted, so each test checks both the request shape and the printed answer.
func askStub(t *testing.T, reply string) *map[string]any {
	t.Helper()
	got := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AT_THREAD_ID", "t1")
	t.Setenv("AT_AGENT_ID", "agent-hub")
	t.Setenv("AT_DAEMON_URL", srv.URL)
	return &got
}

func TestToolAskPostsTheQuestionAndPrintsTheAnswer(t *testing.T) {
	got := askStub(t, `{"ok":true,"answered":true,"selected":["blue"],"text":""}`)
	var code int
	out := captureStdout(t, func() {
		code = Run([]string{"ask", "--kind", "choice", "--question", "Which colour?",
			"--option", "red", "--option", "blue", "--notify", "orchestrator", "--timeout", "90"})
	})
	if code != 0 || out != "answer: blue\n" {
		t.Fatalf("exit = %d, stdout = %q; want 0 and the answer line", code, out)
	}
	want := map[string]any{
		"kind": "ask", "threadId": "t1", "agentId": "agent-hub", "text": "Which colour?",
		"questionKind": "choice", "to": "orchestrator", "timeoutSec": float64(90),
	}
	for k, v := range want {
		if (*got)[k] != v {
			t.Errorf("posted %s = %v, want %v", k, (*got)[k], v)
		}
	}
	if opts := (*got)["options"]; !reflect.DeepEqual(opts, []any{"red", "blue"}) {
		t.Errorf("posted options = %v, want [red blue]", opts)
	}
}

func TestToolAskPrintsEveryShapeOfAnswer(t *testing.T) {
	cases := map[string]struct {
		reply string
		out   string
	}{
		"multi":        {`{"answered":true,"selected":["a","c"]}`, "answer: a, c\n"},
		"text":         {`{"answered":true,"text":"use postgres"}`, "answer: use postgres\n"},
		"choice other": {`{"answered":true,"text":"neither"}`, "answer: neither\n"},
		"option note":  {`{"answered":true,"selected":["a"],"text":"but only for tests"}`, "answer: a\nnote: but only for tests\n"},
	}
	for name, tc := range cases {
		askStub(t, tc.reply)
		var code int
		out := captureStdout(t, func() { code = Run([]string{"ask", "--kind", "text", "--question", "?"}) })
		if code != 0 || out != tc.out {
			t.Errorf("%s: exit = %d, stdout = %q; want 0 and %q", name, code, out, tc.out)
		}
	}
}

// A dismissal succeeds, so the agent reads it and carries on; it is not a
// failed command.
func TestToolAskDeclineExitsZero(t *testing.T) {
	askStub(t, `{"answered":false,"reason":"declined by user"}`)
	var code int
	out := captureStdout(t, func() { code = Run([]string{"ask", "--kind", "text", "--question", "?"}) })
	if code != 0 || out == "" {
		t.Fatalf("exit = %d, stdout = %q; want 0 with a declined line", code, out)
	}
}

// Any other close means the human never answered: the command fails, with the
// reason on stderr where the agent sees it.
func TestToolAskUnansweredExitsOne(t *testing.T) {
	for _, reason := range []string{"timed out", "turn ended", "asker gone", "daemon restarted"} {
		askStub(t, `{"answered":false,"reason":"`+reason+`"}`)
		var code int
		out := captureStdout(t, func() { code = Run([]string{"ask", "--kind", "text", "--question", "?"}) })
		if code != 1 || out != "" {
			t.Errorf("%s: exit = %d, stdout = %q; want 1 and nothing on stdout", reason, code, out)
		}
	}
}

// A refused question (bad kind, no options) is reported by the daemon and the
// command fails, so the model sees why.
func TestToolAskRefusedByDaemonExitsOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid command: a choice question needs 2 to 12 options", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AT_THREAD_ID", "t1")
	t.Setenv("AT_DAEMON_URL", srv.URL)
	var code int
	captureStdout(t, func() { code = Run([]string{"ask", "--kind", "choice", "--question", "?"}) })
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
