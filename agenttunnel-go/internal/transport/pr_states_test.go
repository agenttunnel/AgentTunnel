package transport

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/scm"
)

func TestThreadStatesMapsPRAndDirtyPerThread(t *testing.T) {
	dirs := map[contracts.ThreadID]string{
		"open":      "/wt/open",
		"merged":    "/wt/merged",
		"noPR":      "/wt/nopr",
		"notReady":  "/wt/unavailable",
		"dirtyNoPR": "/wt/dirty",
		"noTree":    "",
	}
	snaps := map[string]scm.Snapshot{
		"/wt/open": {Status: scm.StatusReady, Request: &scm.Request{
			Number: 12, URL: "https://github.com/o/r/pull/12", State: "open", Draft: true, Review: "approved",
		}},
		"/wt/merged": {Status: scm.StatusReady, Request: &scm.Request{
			Number: 9, URL: "https://github.com/o/r/pull/9", State: "merged", Review: "none",
		}},
		"/wt/nopr":        {Status: scm.StatusNone},
		"/wt/unavailable": {Status: scm.StatusUnavailable},
		"/wt/dirty":       {Status: scm.StatusNone},
	}
	dirty := map[string]bool{"/wt/open": true, "/wt/dirty": true}
	ids := []contracts.ThreadID{"open", "merged", "noPR", "notReady", "dirtyNoPR", "noTree", "unknown"}

	got := threadStates(ids,
		func(id contracts.ThreadID) string { return dirs[id] },
		func(dir string) (scm.Snapshot, bool) {
			snap, ok := snaps[dir]
			return snap, ok
		},
		func(dir string) (bool, bool) {
			d, ok := dirty[dir]
			return d, ok
		},
	)

	wantPR := map[contracts.ThreadID]ThreadPR{
		"open":   {Number: 12, URL: "https://github.com/o/r/pull/12", State: "open", Draft: true, ReviewDecision: "approved"},
		"merged": {Number: 9, URL: "https://github.com/o/r/pull/9", State: "merged", ReviewDecision: "none"},
	}
	wantDirty := map[contracts.ThreadID]bool{"open": true, "merged": false, "noPR": false, "notReady": false, "dirtyNoPR": true}
	if len(got) != len(wantDirty) {
		t.Fatalf("threadStates = %+v, want ids %v", got, wantDirty)
	}
	for id, dirty := range wantDirty {
		st, ok := got[id]
		if !ok {
			t.Fatalf("threadStates missing %s", id)
		}
		if st.Dirty != dirty {
			t.Fatalf("threadStates[%s].Dirty = %v, want %v", id, st.Dirty, dirty)
		}
		if pr, want := st.PR, wantPR[id]; (pr == nil) != (want == (ThreadPR{})) || (pr != nil && *pr != want) {
			t.Fatalf("threadStates[%s].PR = %+v, want %+v", id, pr, want)
		}
	}
}

func TestThreadStatesUncachedDirectoryFailsSoft(t *testing.T) {
	// Neither cache has an answer yet: the thread still comes back, clean and
	// without a PR, so the board places it in Idle instead of erroring.
	got := threadStates([]contracts.ThreadID{"t1"},
		func(contracts.ThreadID) string { return "/wt/t1" },
		func(string) (scm.Snapshot, bool) { return scm.Snapshot{}, false },
		func(string) (bool, bool) { return false, false },
	)
	st, ok := got["t1"]
	if !ok || st.PR != nil || st.Dirty {
		t.Fatalf("threadStates[t1] = %+v, %v; want clean with no PR", st, ok)
	}
}

func TestThreadStatesKeepsDirtyWhenForgeHasNoRequest(t *testing.T) {
	// A forge that answers without a request (gh missing, a transient failure, or
	// a PR that is gone) must not drop the thread's dirty state. The PR is absent
	// for this snapshot; the client treats that as no PR, which is the fail-soft path.
	got := threadStates([]contracts.ThreadID{"t1"},
		func(contracts.ThreadID) string { return "/wt/t1" },
		func(string) (scm.Snapshot, bool) { return scm.Snapshot{Status: scm.StatusError}, true },
		func(string) (bool, bool) { return true, true },
	)
	st := got["t1"]
	if st.PR != nil || !st.Dirty {
		t.Fatalf("threadStates[t1] = %+v; want dirty with no PR", st)
	}
}
