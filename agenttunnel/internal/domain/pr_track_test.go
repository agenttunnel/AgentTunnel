package domain

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// A thread keeps every pull request it opened: tracking a second one must not
// replace the first, and tracking the same URL again records nothing.
func TestPRTrackKeepsEveryRequestOnce(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true

	track := func(number int, url string) []contracts.EventPayload {
		t.Helper()
		ref := contracts.PullRequestRef{Number: number, URL: url}
		events, err := Decide(contracts.Command{Type: contracts.CmdPRTrack, ThreadID: "t1", PullRequest: &ref}, s)
		if err != nil {
			t.Fatalf("track %s: %v", url, err)
		}
		for _, p := range events {
			if err := Project(s, contracts.Event{ThreadID: "t1", Payload: p}); err != nil {
				t.Fatal(err)
			}
		}
		return events
	}

	track(1, "https://github.com/o/r/pull/1")
	track(2, "https://github.com/o/r/pull/2")
	if again := track(1, "https://github.com/o/r/pull/1"); len(again) != 0 {
		t.Fatalf("re-tracking a known URL decided %d events, want none", len(again))
	}

	want := []contracts.PullRequestRef{
		{Number: 1, URL: "https://github.com/o/r/pull/1"},
		{Number: 2, URL: "https://github.com/o/r/pull/2"},
	}
	if len(s.PullRequests) != len(want) || s.PullRequests[0] != want[0] || s.PullRequests[1] != want[1] {
		t.Fatalf("tracked = %+v, want %+v", s.PullRequests, want)
	}
}

func TestPRTrackRejectsIncompleteRefs(t *testing.T) {
	s := NewThreadState("t1")
	s.Exists = true
	for _, ref := range []*contracts.PullRequestRef{nil, {Number: 3}, {URL: "https://github.com/o/r/pull/3"}} {
		_, err := Decide(contracts.Command{Type: contracts.CmdPRTrack, ThreadID: "t1", PullRequest: ref}, s)
		if err == nil {
			t.Fatalf("ref %+v accepted, want rejection", ref)
		}
	}
}

// The thread snapshot a reader gets must not share its PR slice with the engine.
func TestPRTrackCloneIsIndependent(t *testing.T) {
	s := NewThreadState("t1")
	s.PullRequests = []contracts.PullRequestRef{{Number: 1, URL: "https://github.com/o/r/pull/1"}}
	c := s.Clone()
	c.PullRequests[0].Number = 99
	if s.PullRequests[0].Number != 1 {
		t.Fatal("Clone shares the PullRequests slice")
	}
}
