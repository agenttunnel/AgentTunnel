package toolbox

import (
	"reflect"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

func TestParseCommandLineDispatch(t *testing.T) {
	call, ok := ParseCommandLine(`$AT_TOOL tool dispatch --role executor --name create-hello --task "Create a file named hello.txt with the content 'hello'" --depends-on plan,review`)
	if !ok {
		t.Fatal("expected a dispatch call")
	}
	if call.Kind != KindDispatch || call.Name != "create-hello" || call.Role != contracts.RoleExecutor {
		t.Fatalf("call = %+v", call)
	}
	if call.Task != "Create a file named hello.txt with the content 'hello'" {
		t.Fatalf("task = %q", call.Task)
	}
	if !reflect.DeepEqual(call.DependsOn, []string{"plan", "review"}) {
		t.Fatalf("dependsOn = %v", call.DependsOn)
	}
}

func TestParseCommandLineFromQuotedBinary(t *testing.T) {
	call, ok := ParseCommandLine(`"$AT_TOOL" tool note --text "found the parser bug"`)
	if !ok || call.Kind != KindNote || call.Text != "found the parser bug" {
		t.Fatalf("call = %+v ok=%v", call, ok)
	}
}

func TestParseCommandLineFromAtdPath(t *testing.T) {
	call, ok := ParseCommandLine(`/usr/local/bin/atd tool learn --text "prefer the existing helper"`)
	if !ok || call.Kind != KindLearn || call.Text != "prefer the existing helper" {
		t.Fatalf("call = %+v ok=%v", call, ok)
	}
}

func TestParseCommandLineMessage(t *testing.T) {
	call, ok := ParseCommandLine(`$AT_TOOL tool message --to researcher --text "the build is broken"`)
	if !ok {
		t.Fatal("expected a message call")
	}
	if call.Kind != KindMessage || call.To != "researcher" || call.Text != "the build is broken" {
		t.Fatalf("call = %+v", call)
	}

	// The bare form a model prints when it never had the $AT_TOOL path.
	bare, ok := ParseCommandLine(`tool message --to orchestrator --text "shipping now"`)
	if !ok || bare.Kind != KindMessage || bare.To != "orchestrator" || bare.Text != "shipping now" {
		t.Fatalf("bare call = %+v ok=%v", bare, ok)
	}

	for _, line := range []string{
		`tool message --to researcher`,
		`tool message --text "no recipient"`,
		`$AT_TOOL tool message`,
	} {
		if call, ok := ParseCommandLine(line); ok {
			t.Fatalf("%q parsed as %+v", line, call)
		}
	}
}

// A printed revive line parses like a printed message: --to and --text are
// both required, so a lead that only echoes the command text still revives.
func TestParseCommandLineRevive(t *testing.T) {
	call, ok := ParseCommandLine(`$AT_TOOL tool revive --to hub --text "which file did you touch?"`)
	if !ok {
		t.Fatal("expected a revive call")
	}
	if call.Kind != KindRevive || call.To != "hub" || call.Text != "which file did you touch?" {
		t.Fatalf("call = %+v", call)
	}
	if call, ok := ParseCommandLine(`tool revive --to hub`); ok {
		t.Fatalf("revive without a question parsed as %+v", call)
	}
	if call, ok := ParseCommandLine(`tool revive --text "no target"`); ok {
		t.Fatalf("revive without a target parsed as %+v", call)
	}
}

func TestParseCommandLineIgnoresProse(t *testing.T) {
	for _, line := range []string{
		"I will use the tool dispatch flow.",
		"tool dispatch needs a task",
		"$AT_TOOL tool dispatch",
		"$AT_TOOL tool spawn",
		"$AT_TOOL tool unknown --text x",
		"tool approve something",
	} {
		if call, ok := ParseCommandLine(line); ok {
			t.Fatalf("%q parsed as %+v", line, call)
		}
	}
}

func TestParseCommandLineSpawn(t *testing.T) {
	call, ok := ParseCommandLine(`"$AT_TOOL" tool spawn --role executor --name inner --task "write the inner tests" --depends-on outer`)
	if !ok {
		t.Fatal("expected a spawn call")
	}
	if call.Kind != KindSpawn || call.Name != "inner" || call.Role != contracts.RoleExecutor {
		t.Fatalf("call = %+v", call)
	}
	if call.Task != "write the inner tests" {
		t.Fatalf("task = %q", call.Task)
	}
	if !reflect.DeepEqual(call.DependsOn, []string{"outer"}) {
		t.Fatalf("dependsOn = %v", call.DependsOn)
	}
}

func TestParseCommandLineSpawnRequiresTask(t *testing.T) {
	if call, ok := ParseCommandLine(`$AT_TOOL tool spawn --name inner`); ok {
		t.Fatalf("spawn without a task parsed as %+v", call)
	}
}

func TestParseCommandLineApproveAndRejectRequireName(t *testing.T) {
	call, ok := ParseCommandLine(`$AT_TOOL tool approve --name inner`)
	if !ok || call.Kind != KindApprove || call.Name != "inner" {
		t.Fatalf("call = %+v ok=%v", call, ok)
	}
	if call.Role != "" {
		t.Fatalf("approve should not carry a role: %+v", call)
	}
	call, ok = ParseCommandLine(`$AT_TOOL tool reject --name inner`)
	if !ok || call.Kind != KindReject || call.Name != "inner" {
		t.Fatalf("call = %+v ok=%v", call, ok)
	}
	if call, ok := ParseCommandLine(`$AT_TOOL tool approve`); ok {
		t.Fatalf("approve without a name parsed as %+v", call)
	}
	if call, ok := ParseCommandLine(`$AT_TOOL tool reject`); ok {
		t.Fatalf("reject without a name parsed as %+v", call)
	}
}
