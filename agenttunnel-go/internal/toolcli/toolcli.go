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
)

// Run executes the `tool` subcommand and returns a process exit code.
func Run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tool <dispatch|note|progress|done|learn|message|spawn|approve|reject|task-create|task-update|task-list|subthread> [flags]")
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
	to := fs.String("to", "", "recipient agent name, or 'orchestrator' (for message)")
	status := fs.String("status", "", "todo|doing|done (for task-update)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	payload := map[string]any{
		"threadId": os.Getenv("AT_THREAD_ID"),
		"agentId":  os.Getenv("AT_AGENT_ID"),
		"kind":     kind,
		"name":     *name,
		"lane":     *lane,
		"role":     *role,
		"task":     *task,
		"harness":  *harness,
		"model":    *model,
		"text":     *text,
		"to":       *to,
		"status":   *status,
	}
	if *summary != "" {
		payload["text"] = *summary
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
	if kind == "task-list" {
		// A list is the answer itself, so it is printed as the daemon sends it.
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tool: %v\n", err)
			return 1
		}
		fmt.Println(strings.TrimSpace(string(out)))
		return 0
	}
	fmt.Println("ok")
	return 0
}
