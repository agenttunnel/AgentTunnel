package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// contractsLogEntry builds a log line for the formatter tests.
func contractsLogEntry(agent string, kind contracts.LogKind, text string, at time.Time) contracts.LogEntry {
	return contracts.LogEntry{AgentID: contracts.AgentID(agent), Kind: kind, Text: text, At: at}
}

func TestThreadRefResolution(t *testing.T) {
	rows := []threadListRow{
		{ID: "th_9f2a1c7d", Title: "nightly audit"},
		{ID: "th_9f2a1c8e", Title: "nightly cleanup"},
		{ID: "th_41b0de22", Title: "fix auth"},
	}
	cases := []struct {
		ref    string
		want   string
		fails  bool
		hasAll []string
	}{
		{ref: "th_9f2a1c7d", want: "th_9f2a1c7d"}, // exact id
		{ref: "th_41b0", want: "th_41b0de22"},     // unique id prefix
		{ref: "41b0de", want: "th_41b0de22"},      // unique body prefix
		{ref: "fix auth", want: "th_41b0de22"},    // exact title
		{ref: "fix au", want: "th_41b0de22"},      // unique title substring
		{ref: "nightly", fails: true, hasAll: []string{"ambiguous", "th_9f2a1c7d", "th_9f2a1c8e"}},
		{ref: "nope", fails: true, hasAll: []string{"no thread matches"}},
		{ref: "  ", fails: true},
	}
	for _, tc := range cases {
		got, err := matchThreadRow(rows, tc.ref)
		if tc.fails {
			if err == nil {
				t.Fatalf("matchThreadRow(%q) = %q, want an error", tc.ref, got)
			}
			for _, want := range tc.hasAll {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("matchThreadRow(%q) error %q missing %q", tc.ref, err, want)
				}
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("matchThreadRow(%q) = %q, %v; want %q", tc.ref, got, err, tc.want)
		}
	}

	// An empty ref is a usage error (exit 2), not a plain failure, so a shell
	// script can tell "you forgot the argument" from "nothing matched".
	_, err := matchThreadRow(rows, "")
	var ee exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("empty ref = %v, want a usage error", err)
	}
}

func TestResolveThreadRowReadsTheListing(t *testing.T) {
	fake := &fakeCLICaller{results: map[string]string{
		"thread.list": `[{"id":"th_abc123456789","title":"nightly audit","projectId":"p1","runningAgents":2,"agents":3}]`,
	}}
	row, err := resolveThreadRow(context.Background(), fake, "th_abc")
	if err != nil {
		t.Fatalf("resolveThreadRow: %v", err)
	}
	if row.ID != "th_abc123456789" || row.ProjectID != "p1" || row.Agents != 3 {
		t.Fatalf("row = %+v", row)
	}
}

func TestPsRendering(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	fake := &fakeCLICaller{results: map[string]string{
		"thread.list": `[
			{"id":"th_9f2a1c7d0000","title":"nightly audit","projectId":"p1","runningAgents":1,"agents":3,"updatedAt":"2026-11-11T11:59:48Z"},
			{"id":"th_41b0de220000","title":"fix worktree","projectId":"p1","agents":2,"updatedAt":"2026-11-11T11:54:00Z"},
			{"id":"th_old000000000","title":"ancient","projectId":"p2","agents":1,"stale":true,"updatedAt":"2026-11-01T00:00:00Z"}
		]`,
		"project.list": `[{"id":"p1","name":"acme","path":"/code/acme"},{"id":"p2","name":"other","path":"/code/other"}]`,
	}}
	env, out := newUsageTestEnv()
	if err := renderPs(context.Background(), env, fake, psOptions{}, now); err != nil {
		t.Fatalf("renderPs: %v", err)
	}
	got := out.String()

	// docker-style header and status words, stale hidden by default.
	for _, want := range []string{"ID", "TITLE", "PROJECT", "STATUS", "AGENTS", "UPDATED", "running", "idle", "acme", "1/3", "0/2", "12s ago", "6m ago"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ancient") || strings.Contains(got, "stale") {
		t.Fatalf("default ps must hide stale threads:\n%s", got)
	}
	// Ids are 12 chars, docker's short form.
	if !strings.Contains(got, "th_9f2a1c7d0") || strings.Contains(got, "th_9f2a1c7d0\n") {
		t.Fatalf("id should be the 12-char short form:\n%s", got)
	}

	envAll, outAll := newUsageTestEnv()
	if err := renderPs(context.Background(), envAll, fake, psOptions{all: true}, now); err != nil {
		t.Fatalf("renderPs -a: %v", err)
	}
	all := outAll.String()
	if !strings.Contains(all, "ancient") || !strings.Contains(all, "stale") || !strings.Contains(all, "10d ago") {
		t.Fatalf("-a must include stale threads with their age:\n%s", all)
	}
}

func TestPsEmptyStates(t *testing.T) {
	fake := &fakeCLICaller{results: map[string]string{
		"thread.list":  `[]`,
		"project.list": `[]`,
	}}
	env, out := newUsageTestEnv()
	if err := renderPs(context.Background(), env, fake, psOptions{}, time.Now()); err != nil {
		t.Fatalf("renderPs: %v", err)
	}
	if !strings.Contains(out.String(), "-a") {
		t.Fatalf("default empty state should hint at -a:\n%s", out.String())
	}

	envAll, outAll := newUsageTestEnv()
	if err := renderPs(context.Background(), envAll, fake, psOptions{all: true}, time.Now()); err != nil {
		t.Fatalf("renderPs -a: %v", err)
	}
	if !strings.Contains(outAll.String(), "no threads yet") {
		t.Fatalf("empty -a state = %s", outAll.String())
	}
}

func TestPsFallsBackToProjectIDWhenProjectIsGone(t *testing.T) {
	fake := &fakeCLICaller{results: map[string]string{
		"thread.list":  `[{"id":"th_1","title":"orphan","projectId":"proj_deleted","agents":0}]`,
		"project.list": `[]`,
	}}
	env, out := newUsageTestEnv()
	if err := renderPs(context.Background(), env, fake, psOptions{}, time.Now()); err != nil {
		t.Fatalf("renderPs: %v", err)
	}
	if !strings.Contains(out.String(), "proj_deleted") {
		t.Fatalf("an unregistered project must still be named:\n%s", out.String())
	}
}

func TestParsePsArgs(t *testing.T) {
	for _, arg := range []string{"-a", "--all"} {
		opts, err := parsePsArgs([]string{arg})
		if err != nil || !opts.all {
			t.Fatalf("parsePsArgs(%q) = %+v, %v", arg, opts, err)
		}
	}
	if _, err := parsePsArgs([]string{"--bogus"}); err == nil {
		t.Fatal("unknown ps flag should fail")
	}
}

func TestShortDurationAndAgo(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{d: 0, want: "0s"},
		{d: 12 * time.Second, want: "12s"},
		{d: 59 * time.Second, want: "59s"},
		{d: 60 * time.Second, want: "1m"},
		{d: 90 * time.Minute, want: "1h"},
		{d: 49 * time.Hour, want: "2d"},
		{d: -5 * time.Second, want: "0s"},
	}
	for _, tc := range cases {
		if got := shortDuration(tc.d); got != tc.want {
			t.Fatalf("shortDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}

	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	if got := relativeAgo(now.Add(-90*time.Second), now); got != "1m ago" {
		t.Fatalf("relativeAgo = %q", got)
	}
	if got := relativeAgo(time.Time{}, now); got != "-" {
		t.Fatalf("unknown time should be a dash, got %q", got)
	}
}

func TestLogEntryFormatting(t *testing.T) {
	env, out := newUsageTestEnv()
	entry := contractsLogEntry("executor-1", "tool", "$ go test ./...\nexit status 1", time.Date(2026, 11, 11, 20, 41, 2, 0, time.UTC))
	writeLogEntry(env.Out, entry)
	got := out.String()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("multi-line entry should print two lines:\n%s", got)
	}
	// Log times render in the local zone, so build the expected stamp from the
	// same instant rather than hard-coding it.
	stamp := time.Date(2026, 11, 11, 20, 41, 2, 0, time.UTC).Local().Format("15:04:05")
	if !strings.HasPrefix(lines[0], stamp+" executor-1 tool $ go test ./...") {
		t.Fatalf("first line = %q, want the %s prefix", lines[0], stamp)
	}
	if !strings.HasPrefix(lines[1], strings.Repeat(" ", len(stamp+" executor-1 tool "))+"exit status 1") {
		t.Fatalf("continuation line should be indented under the prefix: %q", lines[1])
	}
}

func TestLogDeduperKeepsRepeatedLines(t *testing.T) {
	backlog := []contracts.LogEntry{
		contractsLogEntry("a1", "text", "hello", time.Time{}),
		contractsLogEntry("a1", "text", "hello", time.Time{}),
	}
	d := newLogDeduper(backlog)
	if !d.duplicate("a1", "text", "hello") {
		t.Fatal("first repeat should match the backlog")
	}
	if !d.duplicate("a1", "text", "hello") {
		t.Fatal("second repeat should match the second backlog line")
	}
	if d.duplicate("a1", "text", "hello") {
		t.Fatal("a third copy is new output, not a duplicate")
	}
	if d.duplicate("a2", "text", "hello") {
		t.Fatal("a different agent's line is not a duplicate")
	}
}

func TestTurnTrackerEndConditions(t *testing.T) {
	// A roster that is already terminal does not end a follow that must first
	// see the new turn start.
	fresh := newTurnTracker(map[string]contracts.AgentStatus{"a": contracts.AgentSucceeded}, true)
	if fresh.ended() {
		t.Fatal("requireActivity must hold until the turn shows activity")
	}
	fresh.observe("a", contracts.AgentRunning)
	if fresh.ended() {
		t.Fatal("a running agent is not an ended turn")
	}
	fresh.observe("a", contracts.AgentSucceeded)
	if !fresh.ended() {
		t.Fatal("a terminal roster after activity ends the turn")
	}

	// A turn that starts and finishes between two polls is still notice: the
	// polled snapshot differs from the roster at follow start.
	missed := newTurnTracker(map[string]contracts.AgentStatus{"a": contracts.AgentSucceeded}, true)
	missed.update(map[string]contracts.AgentStatus{"a": contracts.AgentRunning})
	missed.update(map[string]contracts.AgentStatus{"a": contracts.AgentSucceeded})
	if !missed.ended() {
		t.Fatal("a turn that completed between polls must end the follow")
	}

	// Without requireActivity, an idle roster is simply over.
	idle := newTurnTracker(map[string]contracts.AgentStatus{"a": contracts.AgentSucceeded}, false)
	if !idle.ended() {
		t.Fatal("an idle roster ends a plain follow")
	}
	if newTurnTracker(map[string]contracts.AgentStatus{"a": contracts.AgentRunning}, false).ended() {
		t.Fatal("a running agent is not an ended turn")
	}
}

func TestParseLogsArgs(t *testing.T) {
	opts, err := parseLogsArgs([]string{"th_1", "--follow", "--agent", "executor-1", "--lines", "40"})
	if err != nil {
		t.Fatalf("parseLogsArgs: %v", err)
	}
	if opts.ref != "th_1" || !opts.follow || opts.agent != "executor-1" || opts.lines != 40 {
		t.Fatalf("opts = %+v", opts)
	}
	if opts, err := parseLogsArgs([]string{"-f", "-n", "5", "th_1"}); err != nil || !opts.follow || opts.lines != 5 {
		t.Fatalf("short flags = %+v, %v", opts, err)
	}
	if opts, err := parseLogsArgs([]string{"th_1"}); err != nil || opts.lines != logsDefaultLines || opts.follow {
		t.Fatalf("defaults = %+v, %v", opts, err)
	}
	for _, bad := range [][]string{{}, {"a", "b"}, {"--lines", "x", "t"}, {"--lines", "-1", "t"}, {"--bogus", "t"}} {
		if _, err := parseLogsArgs(bad); err == nil {
			t.Fatalf("parseLogsArgs(%v) should fail", bad)
		}
	}
	if _, err := parseLogsArgs([]string{"--lines", "999999", "t"}); err == nil {
		t.Fatal("--lines above the cap should fail")
	}
}
