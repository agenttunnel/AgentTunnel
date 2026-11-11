package orchestrator

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// askHelp documents the ask tool for every agent. Asking is not gated on the peer
// mailbox: a blocked agent needs the human whatever the team's messaging is set to.
const askHelp = "Ask the human only when you are blocked on a decision only they can make, and use it sparingly:\n\n" +
	"  \"$AT_TOOL\" tool ask --kind choice|multi|text --question \"<question>\" [--option \"<answer>\" ...] [--notify <agent>]\n\n" +
	"choice takes one option, multi takes several, text takes a free answer. The command blocks until the human answers and prints the answer, so run it as a normal foreground command. If it reports no answer, carry on on a stated assumption."

// BuildSubagentPrompt assembles a subagent's prompt from its role prompt, its
// task, the inherited context, the shared notebook and the open review threads,
// so every subagent starts informed by what the others have done. The peer message
// tool is documented only when the thread has the peer mailbox enabled, and
// messages teammates sent while this agent was waiting are carried in their own
// section. Steers the team lead sent while the agent was not running come in a
// steering section.
func BuildSubagentPrompt(role contracts.Role, task, inherited, shared, reviews string, peerMailbox bool, teammates []contracts.MailboxPosted, steering []string) string {
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
	if strings.TrimSpace(reviews) != "" {
		b.WriteString("\n\n")
		b.WriteString(reviews)
	}
	if len(teammates) > 0 {
		b.WriteString("\n\n## Messages from teammates\n")
		b.WriteString("These arrived while you were waiting. Weigh them alongside your task:\n\n")
		for _, m := range teammates {
			fmt.Fprintf(&b, "- from %s: %s\n", m.From, m.Body)
		}
	}
	if len(steering) > 0 {
		b.WriteString("\n\n## Steering from the team lead\n")
		b.WriteString("These arrived while your previous run was in progress, so they did not change it. Weigh them now and adjust your work:\n\n")
		for _, s := range steering {
			b.WriteString("- ")
			b.WriteString(s)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n\nGit is the team lead's. Do not run state-changing git (add, commit, push, branch, checkout, switch, reset, restore, rebase, merge, stash, worktree, tag, fetch, pull, clean); the lead commits your work. Read-only git (status, diff, log, show) is fine, and a guard refuses the rest.\n")
	b.WriteString("\nIf your task splits into smaller pieces you may propose your own subagents with:\n")
	b.WriteString("  \"$AT_TOOL\" tool spawn --role executor --name <short-name> --task \"<what it should do>\"\n")
	b.WriteString("A proposal only runs after the team lead approves it; keep working on your own task while you wait. Report progress and findings with ")
	b.WriteString(ToolPrefix)
	b.WriteString(" lines. Record durable, project-agnostic learnings (conventions, gotchas worth keeping) with: \"$AT_TOOL\" tool learn --text \"...\"\n")
	b.WriteString("\n")
	b.WriteString(askHelp)
	b.WriteString("\n")
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
	Subthreads bool
	Team       string
	Pass       int
	Steering   []string
	// Steer offers the steer tool: the thread has subagents to steer.
	Steer          bool
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
	if in.Steer {
		b.WriteString("\n\n## Steering a subagent\n")
		b.WriteString("Send a course correction to an agent you dispatched. A steer does not interrupt a run in progress: the agent reads it when its next run starts, so use it for work still waiting on its dependencies or for a later run. To change running work, dispatch a follow-up with --depends-on instead:\n\n")
		b.WriteString("  \"$AT_TOOL\" tool steer --to <agent name> --text \"<adjustment>\"\n\n")
		b.WriteString("The reply says whether the steer was queued for that agent's next run.")
	}
	b.WriteString("\n\n## Tracking the plan\n")
	b.WriteString("Keep the thread's task list in step with the plan you agreed. Add each task once, then move it as it progresses:\n\n")
	b.WriteString("  \"$AT_TOOL\" tool task-create --name <short-name> --task \"<task title>\"\n")
	b.WriteString("  \"$AT_TOOL\" tool task-update --name <short-name> --status todo|doing|done\n")
	b.WriteString("  \"$AT_TOOL\" tool task-list\n")
	b.WriteString("\n\n## Asking the human\n")
	b.WriteString(askHelp)
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
