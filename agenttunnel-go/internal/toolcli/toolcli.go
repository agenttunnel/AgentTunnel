// Package toolcli implements `atd tool …` — the callable tool an agent runs from
// its shell to dispatch work or report back to the orchestrator. Both the daemon
// and the desktop binary expose it, so whichever one spawned the agent can serve
// as the tool.
package toolcli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
	"github.com/agenttunnel/agenttunnel-go/internal/files"
)

// stringList is a repeatable string flag, as in --option a --option b.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// Run executes the `tool` subcommand and returns a process exit code.
func Run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tool <dispatch|note|progress|done|learn|message|steer|spawn|approve|reject|task-create|task-update|task-list|subthread|ask|review-comment|review-reply|review-resolve|thread-list|thread-delete|thread-relaunch|thread-results|read-file|write-file|shell> [flags]")
		return 2
	}
	kind := args[0]

	fs := flag.NewFlagSet("tool "+kind, flag.ContinueOnError)
	role := fs.String("role", "executor", "role that should run the task")
	name := fs.String("name", "", "short task name, used by --depends-on")
	lane := fs.String("lane", "", "pipeline this task belongs to (for the agents view)")
	task := fs.String("task", "", "task description (for dispatch, spawn, task-create and subthread)")
	harness := fs.String("harness", "", "harness override (for dispatch and spawn)")
	model := fs.String("model", "", "model override (for dispatch and spawn)")
	dependsOn := fs.String("depends-on", "", "comma separated task names this depends on")
	text := fs.String("text", "", "text for a note or progress update")
	summary := fs.String("summary", "", "summary for done")
	to := fs.String("to", "", "recipient agent name, or 'orchestrator' (for message); the subagent to steer (for steer)")
	status := fs.String("status", "", "todo|doing|done (for task-update)")
	question := fs.String("question", "", "the question to ask the human (for ask)")
	questionKind := fs.String("kind", "", "choice|multi|text: the shape of the answer (for ask)")
	var options stringList
	fs.Var(&options, "option", "an answer option; repeat for each (for ask)")
	notify := fs.String("notify", "", "agent or 'orchestrator' that also hears the answer (for ask)")
	timeout := fs.Int("timeout", 0, "seconds to wait for the answer, default 600, max 3600 (for ask); seconds a shell may run, default 120, max 600 (for shell)")
	file := fs.String("file", "", "file a review comment is anchored to (for review-comment)")
	line := fs.Int("line", 0, "line a review comment is anchored to (for review-comment)")
	endLine := fs.Int("end-line", 0, "last line of a review comment's range (for review-comment)")
	side := fs.String("side", "", "old or new: which side of the diff a review comment is on (for review-comment)")
	thread := fs.String("thread", "", "review thread id (for review-reply and review-resolve); the target thread (for thread-relaunch and thread-results)")
	reopen := fs.Bool("reopen", false, "move a resolved review thread back to open (for review-resolve)")
	threadID := fs.String("id", "", "thread to delete (for thread-delete)")
	removeWorktree := fs.Bool("remove-worktree", false, "also remove the deleted thread's worktree (for thread-delete)")
	filePath := fs.String("path", "", "file, relative to the thread's worktree (for read-file and write-file)")
	command := fs.String("command", "", "shell command to run in the worktree (for shell)")
	cwd := fs.String("cwd", "", "directory to run the command in, relative to the worktree (for shell)")
	fromStdin := fs.Bool("stdin", false, "read the file's content from stdin instead of --text (for write-file)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	payload := map[string]any{
		"threadId":       os.Getenv("AT_THREAD_ID"),
		"agentId":        os.Getenv("AT_AGENT_ID"),
		"kind":           kind,
		"name":           *name,
		"lane":           *lane,
		"role":           *role,
		"task":           *task,
		"harness":        *harness,
		"model":          *model,
		"text":           *text,
		"to":             *to,
		"status":         *status,
		"file":           *file,
		"line":           *line,
		"endLine":        *endLine,
		"side":           *side,
		"thread":         *thread,
		"reopen":         *reopen,
		"target":         *threadID,
		"removeWorktree": *removeWorktree,
		"path":           *filePath,
		"command":        *command,
		"cwd":            *cwd,
	}
	if *summary != "" {
		payload["text"] = *summary
	}
	if kind == "ask" {
		payload["text"] = *question
		payload["to"] = *notify
		payload["questionKind"] = *questionKind
		payload["options"] = []string(options)
		payload["timeoutSec"] = *timeout
	}
	if kind == "shell" {
		payload["timeoutSec"] = *timeout
	}
	if kind == "write-file" {
		// The content is exactly one of --text or --stdin. Stdin is read only when
		// asked for, so a call never waits on a pipe that nothing closes. An
		// explicit empty --text truncates the file.
		textSet := false
		fs.Visit(func(f *flag.Flag) { textSet = textSet || f.Name == "text" })
		switch {
		case textSet && *fromStdin:
			fmt.Fprintln(os.Stderr, "tool write-file: pass --text or --stdin, not both")
			return 2
		case *fromStdin:
			data, err := io.ReadAll(io.LimitReader(os.Stdin, files.MaxWriteBytes+1))
			if err != nil {
				fmt.Fprintf(os.Stderr, "tool: reading stdin: %v\n", err)
				return 1
			}
			if len(data) > files.MaxWriteBytes {
				fmt.Fprintf(os.Stderr, "tool write-file: content is over %d bytes\n", files.MaxWriteBytes)
				return 1
			}
			payload["text"] = string(data)
		case !textSet:
			fmt.Fprintln(os.Stderr, "tool write-file: pass --text, or --stdin to read the content from stdin")
			return 2
		}
	}
	if *dependsOn != "" {
		payload["dependsOn"] = strings.Split(*dependsOn, ",")
	}
	if payload["threadId"] == "" {
		fmt.Fprintln(os.Stderr, "tool: AT_THREAD_ID is not set; run this from inside an agent session")
		return 1
	}

	body, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tool: %v\n", err)
		return 1
	}

	base := os.Getenv("AT_DAEMON_URL")
	if base == "" {
		fmt.Fprintln(os.Stderr, "tool: AT_DAEMON_URL is not set")
		return 1
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(base, "/")+"/tool/call", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "tool: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := os.Getenv("AT_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tool: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "tool: %s: %s\n", resp.Status, strings.TrimSpace(string(msg)))
		return 1
	}
	if kind == "ask" {
		return printAnswer(resp.Body)
	}
	if kind == "steer" {
		// The steer's outcome is the only answer the lead needs: whether it was
		// queued for the agent's next run.
		var reply struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&reply); err == nil && reply.Message != "" {
			fmt.Println(reply.Message)
			return 0
		}
	}
	if kind == "task-list" || kind == "thread-list" {
		// A list is the answer itself, so it is printed as the daemon sends it.
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tool: %v\n", err)
			return 1
		}
		fmt.Println(strings.TrimSpace(string(out)))
		return 0
	}
	if kind == "read-file" {
		return printFile(resp.Body)
	}
	if kind == "shell" {
		return printShell(resp.Body)
	}
	if kind == "subthread" {
		// The new thread's id is the only handle on the subthread the lead can
		// quote back, so it is printed instead of a bare ok.
		var started struct {
			ThreadID string `json:"threadId"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&started); err == nil && started.ThreadID != "" {
			fmt.Println(started.ThreadID)
			return 0
		}
	}
	fmt.Println("ok")
	return 0
}

// printFile writes a file's text as it is, so the agent reads the file itself.
// A binary file has no text to show and fails the command; a file cut at the
// read cap is noted on stderr.
func printFile(body io.Reader) int {
	var res files.ReadResult
	if err := json.NewDecoder(body).Decode(&res); err != nil {
		fmt.Fprintf(os.Stderr, "tool read-file: %v\n", err)
		return 1
	}
	if res.Binary {
		fmt.Fprintf(os.Stderr, "tool read-file: %s is a binary file (%d bytes); not printed\n", res.Path, res.Size)
		return 1
	}
	fmt.Print(res.Text)
	if res.Truncated {
		fmt.Fprintf(os.Stderr, "tool read-file: %s is %d bytes; only the first %d were shown\n", res.Path, res.Size, files.MaxReadBytes)
	}
	return 0
}

// shellReply is the daemon's answer to `tool shell`.
type shellReply struct {
	ExitCode  int    `json:"exitCode"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timedOut"`
	Stopped   bool   `json:"stopped"`
}

// printShell writes the command's output, then one status line with its exit
// code. The call itself succeeds whenever the daemon ran the command, so a
// failing command is read from the status line, not from this exit status.
func printShell(body io.Reader) int {
	var reply shellReply
	if err := json.NewDecoder(body).Decode(&reply); err != nil {
		fmt.Fprintf(os.Stderr, "tool shell: %v\n", err)
		return 1
	}
	fmt.Print(reply.Output)
	if reply.Output != "" && !strings.HasSuffix(reply.Output, "\n") {
		fmt.Println()
	}
	status := fmt.Sprintf("exit: %d", reply.ExitCode)
	if reply.TimedOut {
		status += " (timed out)"
	}
	if reply.Stopped {
		status += " (stopped)"
	}
	if reply.Truncated {
		status += " (output truncated)"
	}
	fmt.Println(status)
	return 0
}

// askReply is the daemon's answer to `tool ask`: the answer, or why the
// question closed without one.
type askReply struct {
	Answered bool     `json:"answered"`
	Selected []string `json:"selected"`
	Text     string   `json:"text"`
	Reason   string   `json:"reason"`
}

// printAnswer shows the human's answer on stdout, where the agent reads it. A
// dismissal is printed and succeeds, so the agent carries on without it. Any
// other unanswered close fails the command.
func printAnswer(body io.Reader) int {
	var reply askReply
	if err := json.NewDecoder(body).Decode(&reply); err != nil {
		fmt.Fprintf(os.Stderr, "tool ask: %v\n", err)
		return 1
	}
	if reply.Answered {
		if len(reply.Selected) > 0 {
			fmt.Printf("answer: %s\n", strings.Join(reply.Selected, ", "))
		}
		if reply.Text != "" {
			label := "answer"
			if len(reply.Selected) > 0 {
				label = "note"
			}
			fmt.Printf("%s: %s\n", label, reply.Text)
		}
		return 0
	}
	if reply.Reason == contracts.ReasonQuestionDeclined {
		fmt.Println("declined: the user dismissed the question; continue without an answer")
		return 0
	}
	fmt.Fprintf(os.Stderr, "tool ask: no answer (%s)\n", reply.Reason)
	return 1
}
