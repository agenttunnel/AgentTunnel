package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func init() {
	Register(Command{
		Name:    "models",
		Summary: "Inspect the models each harness reports",
		Subcommands: []Command{
			{Name: "ls", Summary: "List known models, their variants, and role defaults", Usage: "at models ls [--harness <h>]", Run: runModelsLs},
		},
	})
}

// harnessListResult mirrors harness.list: every shipped harness, the subset the
// daemon can run here, the enabled subset, and the discovered models per harness.
type harnessListResult struct {
	All       []string            `json:"all"`
	Available []string            `json:"available"`
	Enabled   []string            `json:"enabled"`
	Models    map[string][]string `json:"models"`
}

// roleDefault is one role's effective runtime. settings.get is where "default"
// and "variant" come from: models themselves carry neither.
type roleDefault struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Variant string `json:"variant"`
	Enabled bool   `json:"enabled"`
}

// roleDefaults reads settings.get's role table.
type roleDefaults struct {
	Roles []roleDefault `json:"roles"`
}

func parseModelsArgs(args []string) (string, error) {
	var harness string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--harness" || strings.HasPrefix(arg, "--harness="):
			value, next, err := usageFlagValue(args, i, "--harness")
			if err != nil {
				return "", err
			}
			harness, i = strings.TrimSpace(value), next
		default:
			return "", usageError("unknown argument %q (usage: at models ls [--harness <h>])", arg)
		}
	}
	return harness, nil
}

func runModelsLs(ctx context.Context, env *Env, args []string) error {
	harness, err := parseModelsArgs(args)
	if err != nil {
		return err
	}
	c, err := connectReadOnly(ctx, env)
	if err != nil {
		return err
	}
	defer c.Close()
	return renderModels(ctx, env, c, harness)
}

func renderModels(ctx context.Context, env *Env, c cliCaller, filter string) error {
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
	if filter != "" && !cliListContains(info.All, filter) {
		return usageError("unknown harness %q (known: %s)", filter, strings.Join(info.All, ", "))
	}

	// Best effort: without settings there are still models, just no defaults.
	var defaults roleDefaults
	_ = c.Call(ctx, "settings.get", nil, &defaults)
	roles := defaults.Roles

	harnesses := info.All
	if len(harnesses) == 0 {
		for h := range info.Models {
			harnesses = append(harnesses, h)
		}
		sort.Strings(harnesses)
	}
	var rows [][]string
	for _, h := range harnesses {
		if filter != "" && h != filter {
			continue
		}
		models := append([]string(nil), info.Models[h]...)
		sort.Strings(models)
		if len(models) == 0 {
			rows = append(rows, []string{h, "-", "-", "-"})
			continue
		}
		for _, model := range models {
			rows = append(rows, []string{
				h,
				model,
				modelVariants(roles, h, model),
				modelDefault(roles, h, model),
			})
		}
	}

	if len(rows) == 0 {
		if filter != "" {
			fmt.Fprintf(env.Out, "no models discovered for %s\n", filter)
			return nil
		}
		fmt.Fprintln(env.Out, "no models discovered yet")
		return nil
	}
	renderDockerTable(env.Out, []string{"HARNESS", "MODEL", "VARIANT", "DEFAULT"}, rows)
	return nil
}

// modelVariants collects the role variants that select this harness+model.
func modelVariants(roles []roleDefault, harness, model string) string {
	seen := map[string]bool{}
	var variants []string
	for _, r := range roles {
		if r.Harness != harness || r.Model != model || r.Variant == "" || seen[r.Variant] {
			continue
		}
		seen[r.Variant] = true
		variants = append(variants, r.Variant)
	}
	sort.Strings(variants)
	if len(variants) == 0 {
		return "-"
	}
	return strings.Join(variants, ",")
}

// modelDefault marks a model an enabled role runs by default.
func modelDefault(roles []roleDefault, harness, model string) string {
	for _, r := range roles {
		if r.Enabled && r.Harness == harness && r.Model == model {
			return "default"
		}
	}
	return "-"
}

func cliListContains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
