package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// CouncilMember is one seat at the table: a role that frames its thinking, the
// runtime it speaks from, and the perspective it argues.
type CouncilMember struct {
	Name        string            `json:"name"`
	Role        contracts.RoleID  `json:"role"`
	Harness     contracts.Harness `json:"harness,omitempty"`
	Model       string            `json:"model,omitempty"`
	Perspective string            `json:"perspective,omitempty"`
}

// CouncilSpec is a debate to run: a question, the members who argue it, and how
// many rounds they revise their positions over before the facilitator
// synthesizes an outcome.
type CouncilSpec struct {
	Topic   string          `json:"topic"`
	Rounds  int             `json:"rounds"`
	Members []CouncilMember `json:"members"`
}

const (
	defaultCouncilRounds = 2
	minCouncilMembers    = 2
	maxCouncilMembers    = 6
	maxCouncilRounds     = 4
)

// normalize fills defaults and bounds the spec. Members default to a
// planner/executor/reviewer triangle; each member without an explicit runtime
// inherits its role's configured runtime.
func (o *Orchestrator) normalizeCouncil(spec *CouncilSpec) error {
	spec.Topic = strings.TrimSpace(spec.Topic)
	if spec.Topic == "" {
		return fmt.Errorf("council topic is required")
	}
	if len(spec.Members) == 0 {
		spec.Members = []CouncilMember{
			{Name: "planner"},
			{Name: "executor"},
			{Name: "reviewer"},
		}
	}
	if len(spec.Members) < minCouncilMembers {
		return fmt.Errorf("a council needs at least %d members", minCouncilMembers)
	}
	if len(spec.Members) > maxCouncilMembers {
		return fmt.Errorf("a council supports at most %d members", maxCouncilMembers)
	}
	if spec.Rounds <= 0 {
		spec.Rounds = defaultCouncilRounds
	}
	if spec.Rounds > maxCouncilRounds {
		spec.Rounds = maxCouncilRounds
	}

	for i := range spec.Members {
		m := &spec.Members[i]
		m.Name = safeName(m.Name)
		if m.Name == "task" {
			m.Name = fmt.Sprintf("member-%d", i+1)
		}
		if m.Role == "" {
			m.Role = contracts.RoleExecutor
		}
		if _, ok := o.runner.roles.Get(m.Role); !ok {
			return fmt.Errorf("council member %q: unknown role %q", m.Name, m.Role)
		}
		if m.Harness != "" && !m.Harness.Valid() {
			return fmt.Errorf("council member %q: unknown harness %q", m.Name, m.Harness)
		}
	}
	return nil
}

// RunCouncil convenes a council: members answer the topic in parallel, then
// debate over further rounds where each sees every other member's position and
// must critique, concede, and converge. The facilitator (a separate synthesis
// pass with no stake in the debate) writes the final word into the thread.
//
// The debate is round-based rather than message-passed because harness agents
// are one-shot CLI processes: parallel rounds give the same adversarial
// pressure while staying honest about what the runtimes can do. Every position
// is kept in shared context under council_round_N, so the debate is observable
// from the context panel while it happens.
func (o *Orchestrator) RunCouncil(ctx context.Context, threadID contracts.ThreadID, spec CouncilSpec) (string, error) {
	if err := o.normalizeCouncil(&spec); err != nil {
		return "", err
	}

	positions := map[string]string{}

	for round := 1; round <= spec.Rounds && ctx.Err() == nil; round++ {
		var tasks []contracts.DispatchTask
		var names []string
		for _, m := range spec.Members {
			name := fmt.Sprintf("council-%s-r%d", m.Name, round)
			names = append(names, name)
			tasks = append(tasks, contracts.DispatchTask{
				AgentID:  contracts.AgentID(fmt.Sprintf("agent-council-%s-r%d", m.Name, round)),
				Name:     name,
				Role:     m.Role,
				Task:     o.councilMemberPrompt(spec, m, round, positions),
				Harness:  m.Harness,
				Model:    m.Model,
				Override: true,
				Pipeline: contracts.PipelineID("council"),
			})
		}

		if err := o.runner.Dispatch(ctx, threadID, tasks); err != nil {
			return "", err
		}
		results, err := o.runner.RunCollect(ctx, threadID, tasks)
		if err != nil && ctx.Err() == nil {
			return "", err
		}

		// Positions accumulate across rounds: a member that converged keeps its
		// last position; one that failed keeps its earlier one.
		for i, m := range spec.Members {
			if text := strings.TrimSpace(results[names[i]]); text != "" {
				positions[m.Name] = text
			}
		}

		// Publish the round so the debate is visible as it happens.
		var sb strings.Builder
		for _, m := range spec.Members {
			if pos := positions[m.Name]; pos != "" {
				fmt.Fprintf(&sb, "### %s\n\n%s\n\n", m.Name, pos)
			}
		}
		if sb.Len() > 0 {
			o.runner.setSharedContext(ctx, threadID, fmt.Sprintf("council_round_%d", round), sb.String(), "")
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	// The facilitator reads the final positions and writes the outcome.
	facilitator := contracts.DispatchTask{
		AgentID:  contracts.AgentID("agent-council-facilitator"),
		Name:     "council-facilitator",
		Role:     contracts.RoleFacilitator,
		Task:     o.councilFacilitatorPrompt(spec, positions),
		Override: true,
		Pipeline: contracts.PipelineID("council"),
	}
	if err := o.runner.Dispatch(ctx, threadID, []contracts.DispatchTask{facilitator}); err != nil {
		return "", err
	}
	consensus, err := o.runner.runTask(ctx, threadID, facilitator)
	if err != nil {
		if ctx.Err() != nil {
			o.runner.setStatus(ctx, threadID, facilitator.AgentID, contracts.AgentCancelled, "turn stopped")
		} else {
			o.runner.setStatus(ctx, threadID, facilitator.AgentID, contracts.AgentFailed, err.Error())
		}
		return "", err
	}
	o.runner.setStatus(ctx, threadID, facilitator.AgentID, contracts.AgentSucceeded, "")
	consensus = strings.TrimSpace(consensus)
	if consensus == "" {
		consensus = "The council ran but its facilitator produced no summary. Positions are preserved in this thread's shared context."
	}
	o.runner.setSharedContext(ctx, threadID, "council_consensus", consensus, "")
	return consensus, nil
}

// councilMemberPrompt frames one seat of one round.
func (o *Orchestrator) councilMemberPrompt(spec CouncilSpec, m CouncilMember, round int, positions map[string]string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are %q, one seat of a council debating a single question.\n", m.Name)
	if p := strings.TrimSpace(m.Perspective); p != "" {
		fmt.Fprintf(&sb, "Your assigned perspective: %s\n", p)
	}
	fmt.Fprintf(&sb, "\nQUESTION:\n%s\n", spec.Topic)

	if len(positions) == 0 {
		sb.WriteString("\nOpen the debate: state your position, your two or three strongest arguments, " +
			"and the main risk or tradeoff you see. Be concrete and opinionated; hedging is not useful here. " +
			"Around 200 words.")
		return sb.String()
	}

	fmt.Fprintf(&sb, "\nROUND %d of %d. Current positions:\n", round, spec.Rounds)
	for _, peer := range spec.Members {
		if pos := positions[peer.Name]; pos != "" {
			fmt.Fprintf(&sb, "\n%s:\n%s\n", peer.Name, pos)
		}
	}
	sb.WriteString("\nRespond to the table: name the positions you disagree with and why, concede where " +
		"another member is right, and revise your own position if you were wrong. End your reply with a " +
		"single final line of the form:\nCONSENSUS: <your current recommendation>\n\nAround 200 words.")
	return sb.String()
}

// councilFacilitatorPrompt frames the neutral synthesis.
func (o *Orchestrator) councilFacilitatorPrompt(spec CouncilSpec, positions map[string]string) string {
	var sb strings.Builder
	sb.WriteString("A council has finished debating. Synthesize the outcome for the user.\n")
	fmt.Fprintf(&sb, "\nQUESTION:\n%s\n", spec.Topic)
	sb.WriteString("\nFINAL POSITIONS:")
	for _, m := range spec.Members {
		if pos := positions[m.Name]; pos != "" {
			fmt.Fprintf(&sb, "\n\n%s:\n%s", m.Name, pos)
		}
	}
	sb.WriteString("\n\nProduce the outcome: state the council's recommendation, note any dissent and who " +
		"holds it, and give 2-4 concrete next steps. Pick the strongest recommendation the positions support " +
		"rather than averaging them; do not invent agreement that is not there. Around 300 words.")
	return sb.String()
}
