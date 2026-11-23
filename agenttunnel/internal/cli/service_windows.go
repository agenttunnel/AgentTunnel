//go:build windows

package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
)

// serviceNote is printed after install; empty on Windows.
const serviceNote = ""

// taskStatusRunning matches the Status line `schtasks /Query /V` shows while
// the task's process is alive. The text is English-only, so a localized Windows
// reports running=false rather than a wrong true.
var taskStatusRunning = regexp.MustCompile(`(?m)^Status:\s+Running`)

// taskScheduler manages the "AgentTunnel atd" logon task.
type taskScheduler struct {
	run serviceRunner
}

func newPlatformService(_ string, r serviceRunner) (serviceManager, error) {
	return &taskScheduler{run: r}, nil
}

// install writes the task definition as UTF-16 with a BOM, which schtasks /XML
// reads, registers it over any existing task, and starts it now.
func (t *taskScheduler) install(ctx context.Context, s serviceSpec) error {
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return err
	}
	xmlPath := filepath.Join(s.StateDir, "atd-task.xml")
	if err := os.WriteFile(xmlPath, encodeUTF16(renderTaskXML(s)), 0o600); err != nil {
		return err
	}
	defer os.Remove(xmlPath)
	if out, err := t.run.run(ctx, "schtasks", "/Create", "/TN", serviceTaskName, "/XML", xmlPath, "/F"); err != nil {
		return unavailableOrErr("schtasks /Create", out, err)
	}
	if out, err := t.run.run(ctx, "schtasks", "/Run", "/TN", serviceTaskName); err != nil {
		return commandErr("schtasks /Run", out, err)
	}
	return nil
}

func (t *taskScheduler) uninstall(ctx context.Context) error {
	st, err := t.status(ctx)
	if err != nil || !st.Installed {
		return err
	}
	_, _ = t.run.run(ctx, "schtasks", "/End", "/TN", serviceTaskName)
	if out, err := t.run.run(ctx, "schtasks", "/Delete", "/TN", serviceTaskName, "/F"); err != nil {
		return commandErr("schtasks /Delete", out, err)
	}
	return nil
}

func (t *taskScheduler) status(ctx context.Context) (serviceStatus, error) {
	st := serviceStatus{Service: serviceTaskName}
	out, err := t.run.run(ctx, "schtasks", "/Query", "/TN", serviceTaskName, "/FO", "LIST", "/V")
	st.Installed = err == nil
	st.Loaded = st.Installed
	st.Running = st.Installed && taskStatusRunning.MatchString(out)
	return st, nil
}
