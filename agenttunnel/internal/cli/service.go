package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// The always-on service runs atd under the OS service manager, so the daemon
// is up from login and comes back after a crash. Renderers here are pure and
// shared by every platform; the per-OS files own the service-manager commands.
//
// Respawn contract. A clean exit (status 0) never restarts the job, and a
// failed exit does:
//   - launchd KeepAlive SuccessfulExit=false: restart on any non-zero exit.
//   - systemd Restart=on-failure: restart on a non-zero exit or a signal.
//   - Task Scheduler RestartOnFailure: restart only a run that failed.
//
// atd yields to a live daemon with exit 0, not a distinct code. A yield is not
// a failure, so under the service it must not respawn: a desktop-owned daemon
// with a service atd that exited non-zero would be relaunched every
// ThrottleInterval (launchd has no start limit) while the desktop owns the
// state dir. A user's `daemon stop` also exits 0, so the daemon stays down until
// `daemon start` or the next login. The same holds after a yield: the service
// atd stays down, and if the daemon that owned the state dir later dies, nothing
// restarts the service until the next login or `daemon install`. This is
// deliberate (document, don't change). The renderer goldens and the tests in
// service_test.go pin the restart keys; the exit-0 yield itself lives in cmd/atd.

// serviceSpec is the atd process a service runs. Every path is absolute.
type serviceSpec struct {
	Binary   string // atd executable
	StateDir string // --home-dir
	LogFile  string // --log-file
}

const (
	serviceLabel    = "com.agenttunnel.atd"     // launchd label
	serviceUnit     = "agenttunnel-atd.service" // systemd --user unit
	serviceTaskName = "AgentTunnel atd"         // Task Scheduler task
)

// serviceStatus is what `daemon status` reports about the service. Service is
// the plist, unit file, or task name the platform identifies it by.
type serviceStatus struct {
	Installed bool   `json:"installed"`
	Loaded    bool   `json:"loaded"`
	Running   bool   `json:"running"`
	Service   string `json:"service"`
}

// serviceManager installs, removes and queries the platform service.
type serviceManager interface {
	install(ctx context.Context, s serviceSpec) error
	uninstall(ctx context.Context) error
	status(ctx context.Context) (serviceStatus, error)
}

// serviceRunner is the exec seam: every service-manager command goes through
// it, so tests record argv instead of running launchctl, systemctl or schtasks.
type serviceRunner interface {
	run(ctx context.Context, name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// serviceCommands is the runner the commands use. Tests replace it.
var serviceCommands serviceRunner = execRunner{}

// serviceArgs is the atd argv every platform runs. Port 0 lets the daemon pick
// a free port and record it in daemon.json, as autostart does.
func serviceArgs(s serviceSpec) []string {
	return []string{"--port", "0", "--home-dir", s.StateDir, "--log-file", s.LogFile}
}

// Service stdio. atd --log-file bounds the output that goes through the log
// package. The service manager also opens the log file for the process's
// stdout and stderr, so panics and output before flag parsing are kept. Limit:
// logrotate renames the file at 5 MB, and the inherited handle keeps writing to
// the renamed backup until the process restarts. Stdio written after a rotation
// therefore lands in <atd.log>.1, not the active file.

// renderLaunchAgent is the LaunchAgent plist. KeepAlive restarts a failed exit
// and ThrottleInterval bounds the restart rate. launchd appends stdio to LogFile.
func renderLaunchAgent(s serviceSpec) string {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + serviceLabel + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range append([]string{s.Binary}, serviceArgs(s)...) {
		b.WriteString("\t\t<string>")
		_ = xml.EscapeText(&b, []byte(a))
		b.WriteString("</string>\n")
	}
	b.WriteString(`	</array>
	<key>WorkingDirectory</key>
	<string>`)
	_ = xml.EscapeText(&b, []byte(s.StateDir))
	b.WriteString(`</string>
	<key>StandardOutPath</key>
	<string>`)
	_ = xml.EscapeText(&b, []byte(s.LogFile))
	b.WriteString(`</string>
	<key>StandardErrorPath</key>
	<string>`)
	_ = xml.EscapeText(&b, []byte(s.LogFile))
	b.WriteString(`</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
</dict>
</plist>
`)
	return b.String()
}

// renderSystemdUnit is the systemd --user unit. The start limit caps restarts
// at five in five minutes, so a crash loop stops instead of spinning. stdout
// and stderr append to LogFile (systemd 240+); see the service stdio note above.
func renderSystemdUnit(s serviceSpec) string {
	args := []string{s.Binary}
	args = append(args, serviceArgs(s)...)
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = systemdQuote(a)
	}
	// Output paths are not quoted, but systemd still expands % specifiers.
	logOut := "append:" + strings.ReplaceAll(s.LogFile, "%", "%%")
	return `[Unit]
Description=AgentTunnel daemon (atd)
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
ExecStart=` + strings.Join(quoted, " ") + `
Restart=on-failure
RestartSec=5
StandardOutput=` + logOut + `
StandardError=` + logOut + `

[Install]
WantedBy=default.target
`
}

// systemdQuote quotes one ExecStart word. systemd expands % specifiers, so a
// literal percent is doubled.
func systemdQuote(a string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(a) + `"`
}

// renderTaskXML is the Task Scheduler definition: start at logon, restart up to
// three times a minute apart after a failure, and never start a second copy.
func renderTaskXML(s serviceSpec) string {
	var args []string
	for _, a := range serviceArgs(s) {
		args = append(args, winArg(a))
	}
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>AgentTunnel daemon (atd), started at logon</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>`)
	_ = xml.EscapeText(&b, []byte(s.Binary))
	b.WriteString(`</Command>
      <Arguments>`)
	_ = xml.EscapeText(&b, []byte(strings.Join(args, " ")))
	b.WriteString(`</Arguments>
    </Exec>
  </Actions>
</Task>
`)
	return b.String()
}

// winArg quotes one command-line word for the Task Scheduler Arguments string.
func winArg(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\"") {
		return a
	}
	return `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
}

// commandErr names the failed service-manager command and its output. The
// output is folded onto one line so the CLI prints a single error line, never a
// multi-line tool dump.
func commandErr(what, out string, err error) error {
	out = strings.Join(strings.Fields(out), " ")
	if out == "" {
		return fmt.Errorf("%s: %w", what, err)
	}
	return fmt.Errorf("%s: %w: %s", what, err, out)
}

// errServiceManagerUnavailable means the platform service manager cannot be
// reached at all: no launchctl, no systemd --user session, no schtasks. It is
// distinct from a command that ran and refused, so the installer can warn and
// continue instead of failing an install whose binaries are already in place.
var errServiceManagerUnavailable = errors.New("service manager unavailable")

// exitServiceUnavailable is the exit status for errServiceManagerUnavailable.
const exitServiceUnavailable = 4

// serviceManagerDownMarkers are the outputs systemd prints when the user
// manager is not reachable (no session bus, or a container without systemd as
// PID 1).
var serviceManagerDownMarkers = []string{"Failed to connect to bus", "not been booted with systemd"}

// isServiceManagerMissing reports whether a failed command means the service
// manager is absent or unreachable, rather than that it refused the request.
func isServiceManagerMissing(out string, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "not found") {
		return true
	}
	for _, m := range serviceManagerDownMarkers {
		if strings.Contains(out, m) {
			return true
		}
	}
	return false
}

// unavailableOrErr is commandErr for a failed install step, classifying a
// missing or unreachable service manager as errServiceManagerUnavailable.
func unavailableOrErr(what, out string, err error) error {
	if isServiceManagerMissing(out, err) {
		return fmt.Errorf("%w: %w", errServiceManagerUnavailable, commandErr(what, out, err))
	}
	return commandErr(what, out, err)
}

// servicePause is the clock seam every platform install waits through. Tests
// replace it so a bounded retry never sleeps for real.
var servicePause = sleepCtx

// sleepCtx waits for d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// serviceSpecFor resolves the atd binary and state paths the service runs.
func serviceSpecFor(env *Env) (serviceSpec, error) {
	bin, err := atdBinary()
	if err != nil {
		return serviceSpec{}, fmt.Errorf("atd was not found beside %s or on PATH: %w", os.Args[0], err)
	}
	if bin, err = filepath.Abs(bin); err != nil {
		return serviceSpec{}, err
	}
	state, err := filepath.Abs(env.StateDir())
	if err != nil {
		return serviceSpec{}, err
	}
	return serviceSpec{Binary: bin, StateDir: state, LogFile: atdLogPath(state)}, nil
}

// refuseInAgentSession stops an agent from installing or removing the user's
// service: that is a decision for the user, not a turn's subprocess.
func refuseInAgentSession() error {
	if os.Getenv("AT_AGENT_ID") != "" {
		return errors.New("daemon service changes are refused in an agent session (AT_AGENT_ID is set)")
	}
	return nil
}

func runDaemonInstall(ctx context.Context, env *Env, _ []string) error {
	if err := refuseInAgentSession(); err != nil {
		return err
	}
	spec, err := serviceSpecFor(env)
	if err != nil {
		return err
	}
	mgr, err := newPlatformService(serviceHome(), serviceCommands)
	if err != nil {
		return err
	}
	if err := mgr.install(ctx, spec); err != nil {
		if errors.Is(err, errServiceManagerUnavailable) {
			return exitError{code: exitServiceUnavailable, err: err}
		}
		return err
	}
	st, err := mgr.status(ctx)
	if err != nil {
		return err
	}
	if env.JSON {
		return env.PrintJSON(st)
	}
	fmt.Fprintf(env.Out, "always-on service installed: %s\n", st.Service)
	if serviceNote != "" {
		fmt.Fprintln(env.Out, serviceNote)
	}
	return nil
}

func runDaemonUninstall(ctx context.Context, env *Env, _ []string) error {
	if err := refuseInAgentSession(); err != nil {
		return err
	}
	mgr, err := newPlatformService(serviceHome(), serviceCommands)
	if err != nil {
		return err
	}
	if err := mgr.uninstall(ctx); err != nil {
		return err
	}
	if env.JSON {
		return env.PrintJSON(map[string]bool{"ok": true, "uninstalled": true})
	}
	fmt.Fprintln(env.Out, "always-on service removed")
	return nil
}

// runDaemonStatusService is `daemon status`: the daemon's own status, then the
// service's. Text appends service lines; JSON adds a "service" key to the
// existing object, so one document is printed either way.
func runDaemonStatusService(ctx context.Context, env *Env, args []string) error {
	var buf bytes.Buffer
	sub := *env
	sub.Out = &buf
	daemonErr := runDaemonStatus(ctx, &sub, args)

	mgr, err := newPlatformService(serviceHome(), serviceCommands)
	if err != nil {
		return err
	}
	st, err := mgr.status(ctx)
	if err != nil {
		return err
	}
	if env.JSON {
		doc := map[string]any{}
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			return fmt.Errorf("daemon status: %w", err)
		}
		doc["service"] = st
		if err := env.PrintJSON(doc); err != nil {
			return err
		}
		return daemonErr
	}
	_, _ = env.Out.Write(buf.Bytes())
	fmt.Fprintf(env.Out, "service: installed=%t loaded=%t running=%t (%s)\n", st.Installed, st.Loaded, st.Running, st.Service)
	return daemonErr
}

// serviceHome is the user's home directory for the per-user service files. The
// lookup error is not fatal here: the platform constructor reports it.
func serviceHome() string {
	home, _ := os.UserHomeDir()
	return home
}

// encodeUTF16 returns s as UTF-16LE with a byte-order mark.
func encodeUTF16(s string) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint16(0xFEFF))
	for _, u := range utf16.Encode([]rune(s)) {
		_ = binary.Write(&b, binary.LittleEndian, u)
	}
	return b.Bytes()
}
