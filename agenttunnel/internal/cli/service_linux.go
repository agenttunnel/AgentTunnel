//go:build linux

package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// serviceNote is printed after install. A --user unit runs only while the user
// has a session, so boot-time start needs lingering, which is documented, not
// enabled: enabling it needs root and is the user's call.
const serviceNote = "note: the service starts at login. To start it at boot without a login, run: loginctl enable-linger $USER"

// systemd manages the agenttunnel-atd.service systemd --user unit.
type systemd struct {
	unit string
	run  serviceRunner
}

func newPlatformService(home string, r serviceRunner) (serviceManager, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		if home == "" {
			return nil, errors.New("cannot locate the home directory for the systemd user unit")
		}
		dir = filepath.Join(home, ".config")
	}
	return &systemd{unit: filepath.Join(dir, "systemd", "user", serviceUnit), run: r}, nil
}

// install writes the unit, reloads systemd, enables it, and restarts it. The
// restart is what makes a re-install pick up a changed unit or binary, since
// enable --now alone starts nothing that is already running.
func (s *systemd) install(ctx context.Context, spec serviceSpec) error {
	if err := os.MkdirAll(filepath.Dir(s.unit), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.unit, []byte(renderSystemdUnit(spec)), 0o644); err != nil {
		return err
	}
	for _, argv := range [][]string{
		{"daemon-reload"},
		{"enable", serviceUnit},
		{"restart", serviceUnit},
	} {
		if out, err := s.systemctl(ctx, argv...); err != nil {
			if isServiceManagerMissing(out, err) {
				// Nothing is enabled, so the unit just written must not look installed.
				_ = os.Remove(s.unit)
			}
			return unavailableOrErr("systemctl --user "+strings.Join(argv, " "), out, err)
		}
	}
	return nil
}

func (s *systemd) uninstall(ctx context.Context) error {
	// disable --now stops the daemon; a unit that was never enabled is not an error.
	_, _ = s.systemctl(ctx, "disable", "--now", serviceUnit)
	if err := os.Remove(s.unit); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	_, _ = s.systemctl(ctx, "daemon-reload")
	return nil
}

func (s *systemd) status(ctx context.Context) (serviceStatus, error) {
	st := serviceStatus{Service: s.unit}
	_, err := os.Stat(s.unit)
	st.Installed = err == nil
	_, err = s.systemctl(ctx, "is-enabled", serviceUnit)
	st.Loaded = err == nil
	out, err := s.systemctl(ctx, "is-active", serviceUnit)
	st.Running = err == nil && strings.TrimSpace(out) == "active"
	return st, nil
}

func (s *systemd) systemctl(ctx context.Context, args ...string) (string, error) {
	return s.run.run(ctx, "systemctl", append([]string{"--user"}, args...)...)
}
