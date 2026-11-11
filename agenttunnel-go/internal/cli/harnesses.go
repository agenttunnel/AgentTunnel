package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

func init() {
	Register(Command{
		Name:    "harness",
		Summary: "Inspect the provider runtimes this daemon can use",
		Subcommands: []Command{
			{Name: "ls", Summary: "List harnesses with availability and enabled state", Usage: "at harness ls", Run: runHarnessLs},
		},
	})
}

func runHarnessLs(ctx context.Context, env *Env, args []string) error {
	if len(args) > 0 {
		return usageError("unknown argument %q (usage: at harness ls)", args[0])
	}
	c, err := connectReadOnly(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()
	return renderHarnesses(ctx, env, c)
}

func renderHarnesses(ctx context.Context, env *Env, c cliCaller) error {
	var raw json.RawMessage
	if err := c.Call(ctx, "harness.list", nil, &raw); err != nil {
		return err
	}
	if env.JSON {
		return writeRawJSON(env.Out, raw)
	}
	var info harnessListResult
	if err := json.Unmarshal(raw, &info); err != nil {
		return fmt.Errorf("harness.list: decode result: %w", err)
	}

	harnesses := info.All
	if len(harnesses) == 0 {
		seen := map[string]bool{}
		for _, h := range info.Available {
			seen[h] = true
		}
		for _, h := range info.Enabled {
			seen[h] = true
		}
		for h := range info.Models {
			seen[h] = true
		}
		for h := range seen {
			harnesses = append(harnesses, h)
		}
		sort.Strings(harnesses)
	}
	if len(harnesses) == 0 {
		fmt.Fprintln(env.Out, "no harnesses reported")
		return nil
	}

	rows := make([][]string, 0, len(harnesses))
	for _, h := range harnesses {
		models := "-"
		if info.Models != nil {
			models = fmt.Sprintf("%d", len(info.Models[h]))
		}
		rows = append(rows, []string{
			h,
			availability(info.Available, h),
			enabledState(info.Enabled, h),
			models,
		})
	}
	renderDockerTable(env.Out, []string{"HARNESS", "AVAILABLE", "ENABLED", "MODELS"}, rows)
	return nil
}

// availability reports whether the harness binary is installed. A null list is
// unknown (the daemon could not answer), not "nothing is installed".
func availability(available []string, harness string) string {
	if available == nil {
		return "-"
	}
	if cliListContains(available, harness) {
		return "yes"
	}
	return "no"
}

// enabledState reports the settings toggle. A null list means the daemon has no
// settings store, so the state is unknown.
func enabledState(enabled []string, harness string) string {
	if enabled == nil {
		return "-"
	}
	if cliListContains(enabled, harness) {
		return "yes"
	}
	return "no"
}
