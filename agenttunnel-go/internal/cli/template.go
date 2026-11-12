package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agenttunnel/agenttunnel-go/internal/template"
)

func init() {
	Register(Command{
		Name:    "template",
		Summary: "Write a commented starter job template",
		Usage:   "agenttunnel template [path] [--force]",
		Run:     runTemplate,
	})
}

func runTemplate(_ context.Context, env *Env, args []string) error {
	force := false
	path := ""
	for _, arg := range args {
		switch {
		case arg == "--force":
			force = true
		case strings.HasPrefix(arg, "-"):
			return usageError("unknown flag %q for agenttunnel template", arg)
		case path != "":
			return usageError("agenttunnel template takes at most one path")
		default:
			path = arg
		}
	}
	if path == "" {
		path = template.DefaultName
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if !force {
		if _, err := os.Stat(abs); err == nil {
			return fmt.Errorf("%s already exists; pass --force to overwrite it", abs)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	stem := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	if err := os.WriteFile(abs, template.Scaffold(stem), 0o644); err != nil {
		return err
	}
	if env.JSON {
		return env.PrintJSON(map[string]string{"path": abs})
	}
	fmt.Fprintf(env.Out, "wrote %s\n", abs)
	fmt.Fprintf(env.Out, "edit it, then agenttunnel run %s\n", abs)
	return nil
}
