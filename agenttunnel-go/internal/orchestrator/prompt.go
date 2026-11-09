package orchestrator

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// BuildSubagentPrompt assembles a subagent's prompt from its role prompt, its
// task, the inherited context, and the shared notebook, so every subagent starts
// informed by what the others have done. The peer message tool is documented only
// when the thread has the peer mailbox enabled, and messages teammates sent while
// this agent was waiting are carried in their own section.
func BuildSubagentPrompt(role contracts.Role, task, inherited, shared string, peerMailbox bool, teammates []contracts.MailboxPosted) string {
	var b strings.Builder
	b.WriteString(role.SystemPrompt)
	b.WriteString("\n\n## Task\n")
	b.WriteString(task)
	if strings.TrimSpace(inherited) != "" {
		b.WriteString("\n\n## Inherited context\n")
		b.WriteString(inherited)
	}
	if strings.TrimSpace(shared) != "" {
		b.WriteString("\n\n")
		b.WriteString(shared)
	}
	if len(teammates) > 0 {
		b.WriteString("\n\n## Messages from teammates\n")
		b.WriteString("These arrived while you were waiting. Weigh them alongside your task:\n\n")
		for _, m := range teammates {
			fmt.Fprintf(&b, "- from %s: %s\n", m.From, m.Body)
		}
	}
	b.WriteString("\n\nIf your task splits into smaller pieces you may propose your own subagents with:\n")
	b.WriteString("  \"$AT_TOOL\" tool spawn --role executor --name <short-name> --task \"<what it should do>\"\n")
	b.WriteString("A proposal only runs after the team lead approves it; keep working on your own task while you wait. Report progress and findings with ")
	b.WriteString(ToolPrefix)
	b.WriteString(" lines. Record durable, project-agnostic learnings (conventions, gotchas worth keeping) with: \"$AT_TOOL\" tool learn --text \"...\"\n")
	if peerMailbox {
		b.WriteString("\nYou can also message another agent (or the team lead) while work is running:\n\n")
		b.WriteString("  \"$AT_TOOL\" tool message --to <agent name or 'orchestrator'> --text \"<what to tell them>\"\n")
	}
	return b.String()
}

// OrchestratorPrompt is everything the lead's pass prompt can carry. It exists
// so BuildOrchestratorPrompt's signature stays readable as sections are added;
// zero values render no section.
type OrchestratorPrompt struct {
	Role          contracts.Role
	UserMessage   string
	Inherited     string
	Shared        string
	Roster        string
	PendingSpawns string
	PeerMessages  string
	PeerMailbox   bool
	// Subthreads offers the subthread tool: the thread may start one.
	Subthreads     bool
	Team           string
	Pass           int
	Steering       []string
	RuntimeChoices string
}

// BuildOrchestratorPrompt assembles the orchestrator's pass prompt, including
// the agents already dispatched so a later pass does not repeat work, any pending
// spawn requests awaiting the lead's decision, any peer messages that arrived
// since the last pass, and any steering the user sent mid-run. The peer message
// tool is documented only when the thread has the peer mailbox enabled.
func BuildOrchestratorPrompt(in OrchestratorPrompt) string {
	role, userMessage, inherited, shared, roster, pass := in.Role, in.UserMessage, in.Inherited, in.Shared, in.Roster, in.Pass
	steering, pending, peerMessages, peerMailbox, runtimeChoices := in.Steering, in.PendingSpawns, in.PeerMessages, in.PeerMailbox, in.RuntimeChoices
	var b strings.Builder
	b.WriteString(role.SystemPrompt)
	if strings.TrimSpace(inherited) != "" {
		b.WriteString("\n\n## Inherited context\n")
		b.WriteString(inherited)
	}
	if strings.TrimSpace(shared) != "" {
		b.WriteString("\n\n")
		b.WriteString(shared)
	}
	if strings.TrimSpace(roster) != "" {
		b.WriteString("\n\n## Agents already dispatched\n")
		b.WriteString(roster)
	}
	if strings.TrimSpace(pending) != "" {
		b.WriteString("\n\n")
		b.WriteString(pending)
	}
	b.WriteString("\n\n## User request\n")
	b.WriteString(userMessage)
	if len(steering) > 0 {
		b.WriteString("\n\n## Steering (sent by the user while this run was in progress)\n")
		for _, s := range steering {
			b.WriteString("- ")
			b.WriteString(s)
			b.WriteString("\n")
		}
		b.WriteString("\nWeigh this now and adjust what you dispatch. If a change must land after work that is already running, dispatch a task with --depends-on <name>. Do not repeat work that is already done.")
	}
	if strings.TrimSpace(peerMessages) != "" {
		b.WriteString("\n\n## Peer messages\n")
		b.WriteString(peerMessages)
		b.WriteString("\nWeigh these now. Messages to you are yours to act on; relay the ones marked for a subagent when you dispatch or reply to it.")
	}
	if strings.TrimSpace(in.Team) != "" {
		b.WriteString("\n\n## Your team\n")
		b.WriteString(in.Team)
	}
	b.WriteString("\n\n## Dispatching work\n")
	b.WriteString("You have a real command for delegating. Run it from your shell:\n\n")
	b.WriteString("  \"$AT_TOOL\" tool dispatch --role executor --name <short-name> --task \"<what to do>\"\n\n")
	b.WriteString("The task runs as its own agent in this project, and its result comes back to you. ")
	b.WriteString("Use --depends-on <name> to order tasks, --role planner|executor|tester|reviewer to pick the role, ")
	b.WriteString("--model <id> to override the model, and --harness <id> to override the harness. ")
	b.WriteString("Model and harness overrides apply when auto mode is on for the thread. Report findings with:\n\n")
	b.WriteString("  \"$AT_TOOL\" tool note --text \"<finding>\"\n\n")
	b.WriteString("and progress with `tool progress --text \"<update>\"`.")
	if peerMailbox {
		b.WriteString("\n\nYou can also send a direct message to an agent you have dispatched — including one still waiting on its dependencies — or reply to one that wrote to you:\n\n")
		b.WriteString("  \"$AT_TOOL\" tool message --to <agent name> --text \"<what to tell them>\"\n\n")
		b.WriteString("A message to a waiting agent is delivered when its work starts, so dispatch first, then message. Messages from your agents arrive under \"## Peer messages\" in a later pass of this run.")
	}
	b.WriteString("\n\n## Tracking the plan\n")
	b.WriteString("Keep the thread's task list in step with the plan you agreed. Add each task once, then move it as it progresses:\n\n")
	b.WriteString("  \"$AT_TOOL\" tool task-create --name <short-name> --task \"<task title>\"\n")
	b.WriteString("  \"$AT_TOOL\" tool task-update --name <short-name> --status todo|doing|done\n")
	b.WriteString("  \"$AT_TOOL\" tool task-list\n")
	if in.Subthreads {
		b.WriteString("\n\n## Subthreads\n")
		b.WriteString("Start a subthread only for work that is genuinely unrelated to this thread's plan and better kept in its own context. ")
		b.WriteString("It runs as a separate thread with its own orchestrator, and its final summary comes back to you as a peer message. ")
		b.WriteString("Related work belongs in dispatched tasks, not a subthread. A thread starts at most " + strconv.Itoa(contracts.MaxSubthreads) + " subthreads, and a subthread cannot start its own:\n\n")
		b.WriteString("  \"$AT_TOOL\" tool subthread --task \"<self-contained brief for the unrelated work>\"\n")
	}
	if strings.TrimSpace(runtimeChoices) != "" {
		b.WriteString("\n\n## Runtime choices\n")
		b.WriteString("Auto mode is ON for this thread. ")
		b.WriteString("You may pick each task's harness and model. Available runtimes:\n")
		b.WriteString(runtimeChoices)
		b.WriteString("\nOmit --model/--harness to use the role's default. Only dispatch harnesses from this list.")
	}
	b.WriteString("\n\nDispatch the work first, then summarise the outcome.")
	if pass > 0 {
		b.WriteString("\n\nThis is pass ")
		b.WriteString(strconv.Itoa(pass + 1))
		b.WriteString(". The agents above have already run or are running — do not dispatch the same work again. ")
		b.WriteString("Only dispatch what is still missing, then summarise the outcome.")
	}
	return b.String()
}

// BuildTitlePrompt asks the model for a short name for the session.
func BuildTitlePrompt(userMessage, summary string) string {
	var b strings.Builder
	b.WriteString("Name this coding session.\n\nThe user asked:\n")
	b.WriteString(clip(userMessage, 500))
	if strings.TrimSpace(summary) != "" {
		b.WriteString("\n\nWhat the team did:\n")
		b.WriteString(clip(summary, 800))
	}
	b.WriteString("\n\nReply with only the title: 3 to 6 words, no quotes, no trailing punctuation, no label such as \"Title:\".")
	return b.String()
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// renderReviewComments formats pending review comments for the shared notebook,
// so the next turn's agents see the human's line-anchored feedback.
func renderReviewComments(comments []contracts.ReviewCommentAdded) string {
	var b strings.Builder
	for _, c := range comments {
		fmt.Fprintf(&b, "- %s:%d — %s\n", c.File, c.Line, c.Body)
	}
	return b.String()
}
