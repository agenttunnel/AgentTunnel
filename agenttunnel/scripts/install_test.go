package scripts

// These tests run install.sh end to end against a fake release tree on disk.
// AGENTTUNNEL_BASE_URL points at file://, the install and app directories live
// under t.TempDir, and the agenttunnel binary in the fake archive is a recorder
// that logs its argv. Nothing here starts a daemon or touches launchctl,
// systemctl, or schtasks: the recorder stands in for the real CLI.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type installEnv struct {
	root    string // temp root holding every directory below
	release string
	install string
	app     string
	log     string // argv log written by the recorder
}

func newInstallEnv(t *testing.T) installEnv {
	t.Helper()
	root := t.TempDir()
	e := installEnv{
		root:    root,
		release: filepath.Join(root, "rel"),
		install: filepath.Join(root, "bin"),
		app:     filepath.Join(root, "apps"),
		log:     filepath.Join(root, "argv.log"),
	}
	if err := os.MkdirAll(e.release, 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

// platform maps the test host to the asset names install.sh asks for.
func platform(t *testing.T) (string, string) {
	t.Helper()
	var osName string
	switch runtime.GOOS {
	case "darwin", "linux":
		osName = runtime.GOOS
	default:
		t.Skipf("install.sh is only exercised on darwin and linux, not %s", runtime.GOOS)
	}
	var arch string
	switch runtime.GOARCH {
	case "amd64", "arm64":
		arch = runtime.GOARCH
	default:
		t.Skipf("no release asset for %s", runtime.GOARCH)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is required to run install.sh")
	}
	return osName, arch
}

// recorderScript is the fake agenttunnel CLI. It appends its argv and whether
// atd is present beside it, then fails only when a marker file asks it to.
func (e installEnv) recorderScript() string {
	return `#!/bin/sh
dir=$(dirname "$0")
{
  printf '%s' "$*"
  if [ -f "$dir/atd" ]; then printf ' atd=present'; else printf ' atd=absent'; fi
  printf '\n'
} >> '` + e.log + `'
case "$*" in
  "daemon install") [ -e '` + e.root + `/fail-install' ] && exit 1 ;;
  "daemon uninstall")
    # A CLI from before the always-on service has no such command: usage error, exit 2.
    [ -e '` + e.root + `/legacy-cli' ] && exit 2
    [ -e '` + e.root + `/fail-uninstall' ] && exit 1 ;;
esac
exit 0
`
}

// writeRelease builds the Go release archive for the host platform. version is
// baked into atd so a test can tell which build is installed.
func (e installEnv) writeRelease(t *testing.T, version string) {
	t.Helper()
	osName, arch := platform(t)
	files := map[string]struct {
		body string
		mode int64
	}{
		"atd":                 {"#!/bin/sh\necho atd " + version + "\n", 0o755},
		"agenttunnel":         {e.recorderScript(), 0o755},
		"agenttunnel-desktop": {"#!/bin/sh\necho desktop " + version + "\n", 0o755},
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"atd", "agenttunnel", "agenttunnel-desktop"} {
		f := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	asset := filepath.Join(e.release, "agenttunnel-go-"+osName+"-"+arch+".tar.gz")
	if err := os.WriteFile(asset, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// run executes install.sh with the fake release and the given extra args and
// env. The ambient AGENTTUNNEL_* and AT_* variables are dropped so the host's
// settings cannot leak in.
func (e installEnv) run(t *testing.T, extra []string, args ...string) (string, error) {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "AGENTTUNNEL_") || strings.HasPrefix(kv, "AT_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+filepath.Join(e.root, "home"),
		"AGENTTUNNEL_BASE_URL=file://"+e.release,
		"AGENTTUNNEL_INSTALL_DIR="+e.install,
		"AGENTTUNNEL_APP_DIR="+e.app,
	)
	env = append(env, extra...)
	cmd := exec.Command("sh", append([]string{"install.sh"}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// argvLines returns the recorder's log, one line per CLI call.
func (e installEnv) argvLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(e.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (e installEnv) mustBeRegularExecutable(t *testing.T, name string) {
	t.Helper()
	info, err := os.Lstat(filepath.Join(e.install, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file (mode %v)", name, info.Mode())
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("%s is not executable (mode %v)", name, info.Mode())
	}
}

func TestInstallRegistersServiceAfterCopyingBinaries(t *testing.T) {
	e := newInstallEnv(t)
	e.writeRelease(t, "v1")

	out, err := e.run(t, nil)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	e.mustBeRegularExecutable(t, "atd")
	e.mustBeRegularExecutable(t, "agenttunnel")

	// The service must be registered once, and only after atd is on disk.
	got := e.argvLines(t)
	if len(got) != 1 || got[0] != "daemon install atd=present" {
		t.Fatalf("CLI calls = %q, want one 'daemon install' with atd already present", got)
	}
}

func TestInstallUpgradeRestartsServiceAndReplacesSymlinkWithoutWritingThrough(t *testing.T) {
	e := newInstallEnv(t)
	e.writeRelease(t, "v1")
	if out, err := e.run(t, nil); err != nil {
		t.Fatalf("first install failed: %v\n%s", err, out)
	}

	// An upgrade with an atd that is a symlink into something else. cp would
	// write through it; install.sh must unlink it first.
	victim := filepath.Join(e.root, "victim")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.install, "atd")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(e.install, "atd")); err != nil {
		t.Fatal(err)
	}

	e.writeRelease(t, "v2")
	if out, err := e.run(t, nil); err != nil {
		t.Fatalf("upgrade failed: %v\n%s", err, out)
	}

	if data, _ := os.ReadFile(victim); string(data) != "keep me" {
		t.Fatalf("install wrote through the old symlink: victim now %q", data)
	}
	e.mustBeRegularExecutable(t, "atd")
	data, err := os.ReadFile(filepath.Join(e.install, "atd"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "atd v2") {
		t.Fatalf("installed atd is not the v2 build: %q", data)
	}

	// Both installs register the service; the second is the upgrade restart.
	got := e.argvLines(t)
	if len(got) != 2 || got[1] != "daemon install atd=present" {
		t.Fatalf("CLI calls = %q, want a second 'daemon install' on upgrade", got)
	}
}

func TestInstallWithNoServiceSkipsRegistration(t *testing.T) {
	e := newInstallEnv(t)
	e.writeRelease(t, "v1")

	out, err := e.run(t, []string{"AGENTTUNNEL_NO_SERVICE=1"})
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	e.mustBeRegularExecutable(t, "atd")
	if got := e.argvLines(t); len(got) != 0 {
		t.Fatalf("AGENTTUNNEL_NO_SERVICE=1 still called the CLI: %q", got)
	}
}

func TestInstallWarnsAndContinuesWhenServiceRegistrationFails(t *testing.T) {
	e := newInstallEnv(t)
	e.writeRelease(t, "v1")
	if err := os.WriteFile(filepath.Join(e.root, "fail-install"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := e.run(t, nil)
	if err != nil {
		t.Fatalf("install failed though only the optional service registration failed: %v\n%s", err, out)
	}
	// The binaries stay installed: curl|sh must leave a working CLI behind.
	e.mustBeRegularExecutable(t, "atd")
	e.mustBeRegularExecutable(t, "agenttunnel")
	if !strings.Contains(out, "warning") || !strings.Contains(out, "AGENTTUNNEL_NO_SERVICE=1") {
		t.Fatalf("warning does not name the opt-out:\n%s", out)
	}
	if !strings.Contains(out, "daemon install") {
		t.Fatalf("warning does not name the manual command:\n%s", out)
	}
	// The CLI was asked once; the failure did not trigger a retry or a second call.
	if got := e.argvLines(t); len(got) != 1 {
		t.Fatalf("CLI calls = %q, want exactly one 'daemon install'", got)
	}
}

func TestInstallSkipsServiceInAgentSession(t *testing.T) {
	e := newInstallEnv(t)
	e.writeRelease(t, "v1")

	out, err := e.run(t, []string{"AT_AGENT_ID=agent-test"})
	if err != nil {
		t.Fatalf("install failed inside an agent session: %v\n%s", err, out)
	}
	e.mustBeRegularExecutable(t, "atd")
	// The CLI refuses daemon install under AT_AGENT_ID, so the script must not call it.
	if got := e.argvLines(t); len(got) != 0 {
		t.Fatalf("install called the CLI inside an agent session: %q", got)
	}
	if !strings.Contains(out, "agent session") || !strings.Contains(out, "daemon install") {
		t.Fatalf("skip is not explained with the manual command:\n%s", out)
	}
}

func TestUninstallRemovesServiceBeforeBinaries(t *testing.T) {
	e := newInstallEnv(t)
	e.writeRelease(t, "v1")
	if out, err := e.run(t, nil); err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}

	out, err := e.run(t, nil, "--uninstall")
	if err != nil {
		t.Fatalf("uninstall failed: %v\n%s", err, out)
	}

	// The service is removed while atd still exists beside the CLI.
	got := e.argvLines(t)
	if len(got) != 2 || got[1] != "daemon uninstall atd=present" {
		t.Fatalf("CLI calls = %q, want 'daemon uninstall' while atd is present", got)
	}
	if _, err := os.Lstat(filepath.Join(e.install, "atd")); !os.IsNotExist(err) {
		t.Fatalf("atd was not removed by uninstall: %v", err)
	}
}

func TestUninstallKeepsBinariesWhenServiceRemovalFails(t *testing.T) {
	e := newInstallEnv(t)
	e.writeRelease(t, "v1")
	if out, err := e.run(t, nil); err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(e.root, "fail-uninstall"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := e.run(t, nil, "--uninstall")
	if err == nil {
		t.Fatalf("uninstall succeeded though daemon uninstall failed:\n%s", out)
	}
	// The service still points at these binaries, so they must stay.
	e.mustBeRegularExecutable(t, "atd")
	e.mustBeRegularExecutable(t, "agenttunnel")
}

// A binary from before the always-on service never registered one, so
// uninstalling that install must remove the files instead of aborting.
func TestUninstallRemovesFilesFromCLIWithoutDaemonUninstall(t *testing.T) {
	e := newInstallEnv(t)
	e.writeRelease(t, "v1")
	if out, err := e.run(t, nil); err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(e.root, "legacy-cli"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := e.run(t, nil, "--uninstall")
	if err != nil {
		t.Fatalf("uninstall failed on a CLI without daemon uninstall: %v\n%s", err, out)
	}
	for _, name := range []string{"atd", "agenttunnel", "agenttunnel-desktop"} {
		if _, err := os.Lstat(filepath.Join(e.install, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was not removed: %v", name, err)
		}
	}
}
