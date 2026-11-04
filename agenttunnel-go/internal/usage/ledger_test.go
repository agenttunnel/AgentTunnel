package usage

import (
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestLedgerSplitsOrchestratorAndSubagents(t *testing.T) {
	l := NewLedger()
	l.Record("orch", contracts.RoleOrchestrator, true, contracts.TokenUsage{InputTokens: 100, OutputTokens: 50})
	l.Record("orch", contracts.RoleOrchestrator, true, contracts.TokenUsage{InputTokens: 10, OutputTokens: 5})
	l.Record("exec", contracts.RoleExecutor, false, contracts.TokenUsage{InputTokens: 200, OutputTokens: 100})

	snap := l.Snapshot()
	if snap.Orchestrator.Total() != 165 {
		t.Fatalf("orchestrator total = %d, want 165", snap.Orchestrator.Total())
	}
	if snap.Subagents.Total() != 300 {
		t.Fatalf("subagent total = %d, want 300", snap.Subagents.Total())
	}
	if snap.Total().Total() != 465 {
		t.Fatalf("grand total = %d, want 465", snap.Total().Total())
	}
	if len(snap.ByAgent) != 2 || snap.ByAgent[0].AgentID != "exec" {
		t.Fatalf("byAgent not sorted/complete: %+v", snap.ByAgent)
	}
	if l.Total().Total() != 465 {
		t.Fatalf("ledger total = %d, want 465", l.Total().Total())
	}
}
