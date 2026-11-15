package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/domain"
)

// manualTicker is the injected status ticker: the test fires ticks itself, so
// the suite never waits on real time.
type manualTicker struct {
	calls  int
	period time.Duration
	tick   chan time.Time
	stops  int
}

func (m *manualTicker) make(d time.Duration) (<-chan time.Time, func()) {
	m.calls++
	m.period = d
	return m.tick, func() { m.stops++ }
}

func statusFixture(t *testing.T, period int) (*domain.Engine, *Dispatcher, *manualTicker, *time.Time) {
	t.Helper()
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)

	now := time.Unix(1_000, 0).UTC()
	ticker := &manualTicker{tick: make(chan time.Time, 1)}
	disp := NewDispatcher(DispatcherDeps{
		Engine:     eng,
		Clock:      func() time.Time { return now },
		StatusTick: ticker.make,
	})

	dispatch := func(cmd contracts.Command) {
		t.Helper()
		if err := eng.Dispatch(ctx, cmd); err != nil {
			t.Fatalf("dispatch %s: %v", cmd.Type, err)
		}
	}
	dispatch(contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "T"})
	dispatch(contracts.Command{ID: "p1", Type: contracts.CmdStatusPeriodSet, ThreadID: "t1", StatusPeriod: &period})
	dispatch(contracts.Command{ID: "u1", Type: contracts.CmdTurnStart, ThreadID: "t1", Text: "go"})
	dispatch(contracts.Command{ID: "a1", Type: contracts.CmdTaskAdd, ThreadID: "t1", TaskAdd: &contracts.TaskAdded{Name: "plan", Title: "Plan"}})
	dispatch(contracts.Command{ID: "a2", Type: contracts.CmdTaskAdd, ThreadID: "t1", TaskAdd: &contracts.TaskAdded{Name: "impl", Title: "Implement"}})
	dispatch(contracts.Command{
		ID: "a3", Type: contracts.CmdTaskStatusSet, ThreadID: "t1",
		TaskStatus: &contracts.TaskStatusChanged{Name: "plan", Status: contracts.TaskDone},
	})
	dispatch(contracts.Command{
		ID: "d1", Type: contracts.CmdTaskDispatch, ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{AgentID: "agent-1", Name: "implement", Role: contracts.RoleExecutor, Task: "do it"},
	})
	dispatch(contracts.Command{
		ID: "s1", Type: contracts.CmdAgentStatusSet, ThreadID: "t1",
		Status: &contracts.AgentStatusChanged{AgentID: "agent-1", Status: contracts.AgentRunning},
	})
	dispatch(contracts.Command{
		ID: "d2", Type: contracts.CmdTaskDispatch, ThreadID: "t1",
		Dispatch: &contracts.DispatchTask{AgentID: "agent-2", Name: "review", Role: contracts.RoleReviewer, Task: "check it"},
	})
	dispatch(contracts.Command{
		ID: "s2", Type: contracts.CmdAgentStatusSet, ThreadID: "t1",
		Status: &contracts.AgentStatusChanged{AgentID: "agent-2", Status: contracts.AgentSucceeded},
	})
	return eng, disp, ticker, &now
}

func TestStatusReportPostsNonUserMessage(t *testing.T) {
	ctx := context.Background()
	eng, disp, ticker, now := statusFixture(t, 30)

	// Subscribe after setup so the only event on this channel is the report.
	events, unsub := eng.Subscribe("t1")
	defer unsub()

	stop := disp.startStatusReports(ctx, "t1")
	if ticker.calls != 1 || ticker.period != 30*time.Second {
		t.Fatalf("ticker calls=%d period=%s, want one 30s ticker", ticker.calls, ticker.period)
	}

	*now = (*now).Add(90 * time.Second)
	ticker.tick <- *now

	ev := awaitQuestionEvent(t, events, contracts.EvtMessageSent, nil)
	msg := ev.Payload.(contracts.MessageSent)
	if msg.Author != contracts.AuthorStatus {
		t.Fatalf("author = %q, want %q", msg.Author, contracts.AuthorStatus)
	}
	for _, want := range []string{"1m30s elapsed", "implement (executor): running", "Settled: 1 (1 succeeded)", "Tasks: 1/2 done"} {
		if !strings.Contains(msg.Text, want) {
			t.Fatalf("status message missing %q:\n%s", want, msg.Text)
		}
	}

	// A status report is not a user message: it must not open a turn.
	st, err := eng.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Turn != 1 {
		t.Fatalf("turn = %d, want 1 (a status report must not open a turn)", st.Turn)
	}

	stop()
	if ticker.stops != 1 {
		t.Fatalf("ticker stops = %d, want 1", ticker.stops)
	}
	// The loop is joined, so a tick after stop has no consumer and no report.
	select {
	case ticker.tick <- *now:
	default:
	}
}

func TestSetStatusPeriodRPC(t *testing.T) {
	ctx := context.Background()
	eng, disp, _, _ := statusFixture(t, 60)

	events, unsub := eng.Subscribe("t1")
	defer unsub()

	params, err := json.Marshal(map[string]any{"threadId": "t1", "seconds": 15})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disp.Handle(ctx, Request{Method: "thread.setStatusPeriod", Params: params}); err != nil {
		t.Fatalf("thread.setStatusPeriod: %v", err)
	}
	ev := awaitQuestionEvent(t, events, contracts.EvtStatusPeriodSet, nil)
	if p := ev.Payload.(contracts.StatusPeriodSet); p.PeriodSeconds != 15 {
		t.Fatalf("period = %d, want 15", p.PeriodSeconds)
	}
	st, err := eng.State(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if st.StatusPeriodSeconds != 15 {
		t.Fatalf("state period = %d, want 15", st.StatusPeriodSeconds)
	}
}

func TestStatusReportOffStartsNoTicker(t *testing.T) {
	ctx := context.Background()
	_, disp, ticker, _ := statusFixture(t, 0)

	stop := disp.startStatusReports(ctx, "t1")
	if ticker.calls != 0 {
		t.Fatalf("ticker created %d time(s) for an off report", ticker.calls)
	}
	stop()
}

func TestStatusReportStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, disp, ticker, _ := statusFixture(t, 15)

	stop := disp.startStatusReports(ctx, "t1")
	cancel()
	stop()
	if ticker.stops != 1 {
		t.Fatalf("ticker stops = %d, want 1", ticker.stops)
	}
}

// reportState reads the fixture thread the way postStatusReport does.
func reportState(t *testing.T, eng *domain.Engine) *domain.ThreadState {
	t.Helper()
	st, err := eng.State(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStatusReportDoneAndTodoSections(t *testing.T) {
	eng, _, _, _ := statusFixture(t, 30)

	got := statusReportText(reportState(t, eng), 90*time.Second)
	want := strings.Join([]string{
		"Workflow status · 1m30s elapsed",
		"Done so far:",
		"- task Plan",
		"- agent review succeeded",
		"Still to do:",
		"- task Implement (todo)",
		"- agent implement (executor): running",
		"Settled: 1 (1 succeeded)",
		"Tasks: 1/2 done",
	}, "\n")
	if got != want {
		t.Fatalf("status report =\n%s\nwant\n%s", got, want)
	}
}

func TestStatusReportWorthKnowing(t *testing.T) {
	ctx := context.Background()
	eng, _, _, _ := statusFixture(t, 30)

	dispatch := func(cmd contracts.Command) {
		t.Helper()
		if err := eng.Dispatch(ctx, cmd); err != nil {
			t.Fatalf("dispatch %s: %v", cmd.Type, err)
		}
	}
	dispatch(contracts.Command{
		ID: "s3", Type: contracts.CmdAgentStatusSet, ThreadID: "t1",
		Status: &contracts.AgentStatusChanged{AgentID: "agent-1", Status: contracts.AgentFailed, Reason: "tests failed\nin pkg/x"},
	})
	dispatch(contracts.Command{
		ID: "q1", Type: contracts.CmdQuestionAsk, ThreadID: "t1",
		QuestionAsk: &contracts.QuestionAsked{QuestionID: "q1", AgentID: "agent-1", Kind: contracts.QuestionText, Question: "Which\nbranch?"},
	})

	got := statusReportText(reportState(t, eng), 0)
	for _, want := range []string{
		"Done so far:\n- task Plan\n- agent review succeeded\n",
		"Still to do:\n- task Implement (todo)\n",
		"Worth knowing:\n- agent implement failed: tests failed in pkg/x\n- question waiting: Which branch?\n",
		"Settled: 2 (1 succeeded, 1 failed)\n",
		"Tasks: 1/2 done",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status report missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "agent implement (executor)") {
		t.Fatalf("failed agent must not also be listed as still to do:\n%s", got)
	}
}

func TestStatusReportEmptyThread(t *testing.T) {
	ctx := context.Background()
	eng := domain.NewEngine(domain.NewMemoryStore(), nil)
	eng.Start(ctx)
	t.Cleanup(eng.Close)
	if err := eng.Dispatch(ctx, contracts.Command{ID: "c1", Type: contracts.CmdThreadCreate, ThreadID: "t1", Title: "T"}); err != nil {
		t.Fatal(err)
	}

	got := statusReportText(reportState(t, eng), 0)
	want := "Workflow status · 0s elapsed\nTasks: 0/0 done"
	if got != want {
		t.Fatalf("empty status report = %q, want %q", got, want)
	}
}
