package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// Reference resolution shared by every command that takes a thread or an
// agent. Ids are long and random; the short form ps prints is a prefix of the
// full id, so any unique prefix resolves. Ambiguity is an error that lists the
// candidates, never a guess.

// resolveThreadRow resolves a thread reference against thread.list, returning
// the row itself so the command can render its title and project without a
// second lookup.
func resolveThreadRow(ctx context.Context, c cliCaller, ref string) (threadListRow, error) {
	var rows []threadListRow
	if err := c.Call(ctx, "thread.list", nil, &rows); err != nil {
		return threadListRow{}, err
	}
	id, err := matchThreadRow(rows, ref)
	if err != nil {
		return threadListRow{}, err
	}
	for _, row := range rows {
		if row.ID == id {
			return row, nil
		}
	}
	return threadListRow{}, fmt.Errorf("thread %q is no longer listed", id)
}

// matchThreadRow is the thread ref resolution: an exact id, then an exact
// title, then a unique id prefix or title substring. A user copying
// `agenttunnel ps`'s 12-character id sees that id truncated at the front, so a
// prefix of the id and a prefix of its non-"th_" body both count.
func matchThreadRow(rows []threadListRow, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", usageError("thread reference is empty")
	}
	lower := strings.ToLower(ref)
	var exactID, exactTitle []threadListRow
	matched := make(map[string]threadListRow)
	for _, row := range rows {
		switch {
		case row.ID == ref:
			exactID = append(exactID, row)
		case row.Title == ref:
			exactTitle = append(exactTitle, row)
		}
		if threadIDMatches(row.ID, ref) || strings.Contains(strings.ToLower(row.Title), lower) {
			matched[row.ID] = row
		}
	}
	if len(exactID) == 1 {
		return exactID[0].ID, nil
	}
	if len(exactTitle) == 1 {
		return exactTitle[0].ID, nil
	}
	if len(matched) == 1 {
		for id := range matched {
			return id, nil
		}
	}
	if len(matched) == 0 {
		return "", fmt.Errorf("no thread matches %q", ref)
	}
	candidates := make([]string, 0, len(matched))
	for _, row := range rows {
		if _, ok := matched[row.ID]; !ok {
			continue
		}
		label := row.ID
		if row.Title != "" {
			label += fmt.Sprintf(" (%q)", row.Title)
		}
		candidates = append(candidates, label)
	}
	return "", fmt.Errorf("thread reference %q is ambiguous; candidates: %s", ref, strings.Join(candidates, ", "))
}

// threadIDMatches accepts a prefix of the id, a prefix of the id without its
// "th_" prefix, or a prefix of the hex body, so both `agenttunnel ps`'s short id
// and a bare hash fragment resolve.
func threadIDMatches(id, ref string) bool {
	if strings.HasPrefix(id, ref) {
		return true
	}
	body := strings.TrimPrefix(id, "th_")
	return body != id && strings.HasPrefix(body, ref)
}

// listAgents reads a thread's agents in roster order.
func listAgents(ctx context.Context, c cliCaller, threadID string) ([]contracts.Agent, error) {
	var views []struct {
		Agent contracts.Agent `json:"agent"`
	}
	if err := c.Call(ctx, "agents.list", map[string]string{"threadId": threadID}, &views); err != nil {
		return nil, err
	}
	agents := make([]contracts.Agent, 0, len(views))
	for _, v := range views {
		agents = append(agents, v.Agent)
	}
	return agents, nil
}

// matchAgent resolves an agent reference within one thread: the exact id (or
// the id without its "agent-" prefix), then the exact name, then a unique id
// prefix. Like threads, ambiguity lists the candidates.
func matchAgent(agents []contracts.Agent, ref string) (contracts.Agent, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return contracts.Agent{}, usageError("agent reference is empty")
	}
	byID := func(a contracts.Agent) bool {
		id := string(a.ID)
		return id == ref || id == "agent-"+ref
	}
	byName := func(a contracts.Agent) bool { return a.Name != "" && a.Name == ref }
	byPrefix := func(a contracts.Agent) bool {
		id := string(a.ID)
		return strings.HasPrefix(id, ref) || strings.HasPrefix(id, "agent-"+ref)
	}
	for _, tier := range []func(contracts.Agent) bool{byID, byName, byPrefix} {
		var hits []contracts.Agent
		for _, a := range agents {
			if tier(a) {
				hits = append(hits, a)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		}
		labels := make([]string, 0, len(hits))
		for _, a := range hits {
			label := string(a.ID)
			if a.Name != "" {
				label += fmt.Sprintf(" (%q)", a.Name)
			}
			labels = append(labels, label)
		}
		return contracts.Agent{}, fmt.Errorf("agent reference %q is ambiguous; candidates: %s", ref, strings.Join(labels, ", "))
	}
	return contracts.Agent{}, fmt.Errorf("no agent matches %q", ref)
}

// resolveAgentID resolves an agent reference against one thread's roster.
func resolveAgentID(ctx context.Context, c cliCaller, threadID, ref string) (contracts.AgentID, error) {
	agents, err := listAgents(ctx, c, threadID)
	if err != nil {
		return "", err
	}
	agent, err := matchAgent(agents, ref)
	if err != nil {
		return "", err
	}
	return agent.ID, nil
}
