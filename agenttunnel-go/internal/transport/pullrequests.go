package transport

import (
	"context"
	"errors"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/scm"
)

// prList answers pr.list for a thread. It records every pull request the forge
// shows for the thread's branch that the thread does not track yet, so earlier
// requests stay tracked when a later one opens, then reads each tracked request.
func (d *Dispatcher) prList(ctx context.Context, req Request) (any, error) {
	var p struct {
		ThreadID contracts.ThreadID `json:"threadId"`
	}
	if err := decode(req.Params, &p); err != nil {
		return nil, err
	}
	if p.ThreadID == "" {
		return nil, errors.New("threadId is required")
	}
	wd := d.threadWorkdir(ctx, p.ThreadID)
	if wd == "" {
		return nil, errors.New("thread has no workdir")
	}

	c := scm.Live()
	list, found := c.Discover(ctx, wd)
	st, err := d.engine.State(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	if list.Status == scm.StatusReady {
		for _, ref := range found {
			if st.HasPullRequest(ref.URL) {
				continue
			}
			if err := d.engine.Dispatch(ctx, contracts.Command{
				ID: d.nextID(), Type: contracts.CmdPRTrack, ThreadID: p.ThreadID, PullRequest: &ref,
			}); err != nil {
				return nil, err
			}
		}
		if st, err = d.engine.State(ctx, p.ThreadID); err != nil {
			return nil, err
		}
	}
	return c.Details(ctx, wd, list, st.PullRequests), nil
}
