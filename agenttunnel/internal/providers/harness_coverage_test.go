package providers

import (
	"slices"
	"testing"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// harnessCoverage pins what each supported harness must provide: its CLI binary
// and whether it can list its own models. Adding a harness to
// contracts.AllHarnesses without a row here fails the tests below.
var harnessCoverage = map[contracts.Harness]struct {
	binary     string
	listsModel bool
}{
	contracts.HarnessOpenCode:    {binary: "opencode", listsModel: true},
	contracts.HarnessClaude:      {binary: "claude", listsModel: false},
	contracts.HarnessCodex:       {binary: "codex", listsModel: false},
	contracts.HarnessCursor:      {binary: "cursor-agent", listsModel: true},
	contracts.HarnessPi:          {binary: "pi", listsModel: true},
	contracts.HarnessCommandCode: {binary: "commandcode", listsModel: true},
}

func TestEverySupportedHarnessHasCoverage(t *testing.T) {
	supported := contracts.AllHarnesses()
	for _, h := range supported {
		if _, ok := harnessCoverage[h]; !ok {
			t.Errorf("supported harness %q has no coverage row", h)
		}
	}
	if len(harnessCoverage) != len(supported) {
		t.Errorf("coverage rows = %d, supported harnesses = %d", len(harnessCoverage), len(supported))
	}
}

func TestEverySupportedHarnessHasDriverAndDiscovery(t *testing.T) {
	reg := BuiltinRegistry()
	for _, h := range contracts.AllHarnesses() {
		want := harnessCoverage[h]
		drv, ok := reg.Get(h)
		if !ok {
			t.Errorf("%q: no driver registered", h)
			continue
		}
		if drv.Harness() != h {
			t.Errorf("%q: driver reports harness %q", h, drv.Harness())
		}
		if drv.Binary() != want.binary {
			t.Errorf("%q: binary = %q, want %q", h, drv.Binary(), want.binary)
		}
		_, discovers := reg.Discoverer(h)
		if discovers != want.listsModel {
			t.Errorf("%q: discovery registered = %v, want %v", h, discovers, want.listsModel)
		}
	}
}

// Every driver must put the requested model on its command line, or a role's
// model choice silently does nothing.
func TestEverySupportedHarnessPassesModelToCLI(t *testing.T) {
	reg := BuiltinRegistry()
	const model = "test-model-7"
	for _, h := range contracts.AllHarnesses() {
		drv, ok := reg.Get(h)
		if !ok {
			t.Errorf("%q: no driver registered", h)
			continue
		}
		spec, _, err := drv.BuildInvocation(SessionSpec{Prompt: "hi", Model: model})
		if err != nil {
			t.Errorf("%q: BuildInvocation: %v", h, err)
			continue
		}
		if spec.Binary != drv.Binary() {
			t.Errorf("%q: invocation binary = %q, want %q", h, spec.Binary, drv.Binary())
		}
		if !slices.Contains(spec.Args, model) {
			t.Errorf("%q: model %q missing from args %v", h, model, spec.Args)
		}
	}
}
