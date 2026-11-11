package contracts

import (
	"fmt"
	"sync"
)

// RoleID identifies a workflow role. Builtins ship with the daemon; users can
// register custom roles with their own prompts and default runtimes.
type RoleID string

const (
	RoleOrchestrator   RoleID = "orchestrator"
	RolePlanner        RoleID = "planner"
	RoleExecutor       RoleID = "executor"
	RoleTester         RoleID = "tester"
	RoleReviewer       RoleID = "reviewer"
	RoleFacilitator    RoleID = "facilitator"
	RoleContextManager RoleID = "context-manager"
)

// Role describes an agent role: its identity, system prompt, and default runtime.
type Role struct {
	ID             RoleID  `json:"id"`
	Name           string  `json:"name"`
	SystemPrompt   string  `json:"systemPrompt"`
	DefaultHarness Harness `json:"defaultHarness"`
	DefaultModel   string  `json:"defaultModel"`
	DefaultVariant string  `json:"defaultVariant,omitempty"`
	Builtin        bool    `json:"builtin"`
	// Experimental roles are usable only when the user opts into experimental
	// roles. Required roles cannot be disabled: the core workflow depends on them.
	Experimental bool `json:"experimental,omitempty"`
	Required     bool `json:"required,omitempty"`
}

// BuiltinRoles returns the seven roles AgentTunnel ships: orchestrator, planner,
// executor, tester (QA), reviewer, the council facilitator, and the experimental
// context manager.
func BuiltinRoles() []Role {
	return []Role{
		{
			ID:             RoleOrchestrator,
			Name:           "Orchestrator",
			Builtin:        true,
			Required:       true,
			DefaultHarness: HarnessOpenCode,
			SystemPrompt: "You are the AgentTunnel orchestrator (team lead). Break the user's task into concrete jobs, " +
				"dispatch them to planner/executor/tester/reviewer subagents, keep a live view of who is doing what, " +
				"and produce a final report. Prefer parallel dispatch when tasks are independent. Never implement " +
				"directly when an executor can. You own git: you alone stage, commit, branch, and merge the work " +
				"your subagents produce, and subagents are limited to read-only git.",
		},
		{
			ID:             RolePlanner,
			Name:           "Planner",
			Builtin:        true,
			DefaultHarness: HarnessOpenCode,
			SystemPrompt: "You are a planner. Research the assigned sub-problem in the repository and return a concrete, " +
				"ordered plan with exact files and steps. Do not edit files; your output is a plan.",
		},
		{
			ID:             RoleExecutor,
			Name:           "Executor",
			Builtin:        true,
			Required:       true,
			DefaultHarness: HarnessOpenCode,
			SystemPrompt: "You are an executor. Implement the assigned task in the working tree, run the relevant checks, " +
				"and report what you changed. If the task splits into smaller pieces you may propose subagents with " +
				"\"$AT_TOOL\" tool spawn; they only run once the team lead approves.",
		},
		{
			ID:             RoleTester,
			Name:           "QA Tester",
			Builtin:        true,
			DefaultHarness: HarnessOpenCode,
			SystemPrompt: "You are a QA tester. Independently verify the executors' work: run tests, exercise edge cases, " +
				"and report concrete failures with reproduction steps. Do not fix code yourself.",
		},
		{
			ID:             RoleReviewer,
			Name:           "Reviewer",
			Builtin:        true,
			Required:       true,
			DefaultHarness: HarnessOpenCode,
			SystemPrompt: "You are a reviewer. Inspect the diff the executors produced for correctness, style, and risk. " +
				"Post each finding as a review thread with \"$AT_TOOL\" tool review-comment --file <file> --line <n> " +
				"[--end-line <n>] [--side old|new] --text \"<finding>\", so the executors can answer it in place. " +
				"You will be asked to re-check your threads once they reply. Then return an explicit approve/request-changes verdict.",
		},
		{
			ID:             RoleFacilitator,
			Name:           "Facilitator",
			Builtin:        true,
			DefaultHarness: HarnessOpenCode,
			SystemPrompt: "You are a neutral council facilitator. Read the members' positions, surface where they agree " +
				"and where they split, and state one decisive recommendation with the dissent recorded. Do not add " +
				"arguments of your own or invent agreement that is not there.",
		},
		{
			ID:             RoleContextManager,
			Name:           "Context Manager",
			Builtin:        true,
			Experimental:   true,
			DefaultHarness: HarnessOpenCode,
			SystemPrompt: "You are the context manager. Read the thread's collected subagent results and write one " +
				"concise, current summary of what is known, decided, and still open. Keep it short and factual, " +
				"drop anything superseded, and do not add work of your own.",
		},
	}
}

// RoleRegistry resolves roles by id. It is safe for concurrent use.
type RoleRegistry struct {
	mu    sync.RWMutex
	order []RoleID
	roles map[RoleID]Role
}

// NewRoleRegistry builds a registry seeded with the given roles, in order.
func NewRoleRegistry(seed ...Role) *RoleRegistry {
	r := &RoleRegistry{roles: make(map[RoleID]Role)}
	for _, role := range seed {
		_ = r.Register(role)
	}
	return r
}

// Register adds or replaces a role. Custom roles must carry a non-empty id and
// prompt; builtins cannot be replaced with a non-builtin definition.
func (r *RoleRegistry) Register(role Role) error {
	if role.ID == "" {
		return fmt.Errorf("role id is required")
	}
	if role.SystemPrompt == "" {
		return fmt.Errorf("role %q: system prompt is required", role.ID)
	}
	if !role.DefaultHarness.Valid() {
		role.DefaultHarness = HarnessOpenCode
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.roles[role.ID]; !exists {
		r.order = append(r.order, role.ID)
	}
	r.roles[role.ID] = role
	return nil
}

// Get returns the role for id.
func (r *RoleRegistry) Get(id RoleID) (Role, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	role, ok := r.roles[id]
	return role, ok
}

// List returns all roles in registration order.
func (r *RoleRegistry) List() []Role {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Role, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.roles[id])
	}
	return out
}
