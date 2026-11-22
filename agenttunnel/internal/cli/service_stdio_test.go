package cli

import (
	"strings"
	"testing"
)

// The service manager appends the daemon's stdout and stderr to the log file,
// so panics and pre-flag output are not lost. Both keys must point at LogFile.
func TestLaunchAgentStdioGoesToLogFile(t *testing.T) {
	out := renderLaunchAgent(goldenSpec)
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		want := "<key>" + key + "</key>\n\t<string>" + goldenSpec.LogFile + "</string>"
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %s pointing at the log file:\n%s", key, out)
		}
	}
}

func TestLaunchAgentStdioEscapesXML(t *testing.T) {
	spec := goldenSpec
	spec.LogFile = "/Users/a&b/atd.log"
	out := renderLaunchAgent(spec)
	// The path also rides in ProgramArguments, so pin the stdio keys themselves.
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		want := "<key>" + key + "</key>\n\t<string>/Users/a&amp;b/atd.log</string>"
		if !strings.Contains(out, want) {
			t.Errorf("%s is not XML-escaped in the plist:\n%s", key, out)
		}
	}
	if strings.Contains(out, "a&b") {
		t.Errorf("raw ampersand leaked into the plist:\n%s", out)
	}
}

func TestSystemdUnitStdioAppendsToLogFile(t *testing.T) {
	out := renderSystemdUnit(goldenSpec)
	for _, want := range []string{
		"StandardOutput=append:" + goldenSpec.LogFile,
		"StandardError=append:" + goldenSpec.LogFile,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unit missing %q", want)
		}
	}
}

// systemd expands % specifiers in these values, so a literal percent must be
// doubled or the log path is rewritten.
func TestSystemdUnitStdioEscapesPercent(t *testing.T) {
	spec := goldenSpec
	spec.LogFile = "/home/u/100%/atd.log"
	out := renderSystemdUnit(spec)
	if !strings.Contains(out, "StandardOutput=append:/home/u/100%%/atd.log") {
		t.Errorf("percent not doubled in StandardOutput:\n%s", out)
	}
	if !strings.Contains(out, "StandardError=append:/home/u/100%%/atd.log") {
		t.Errorf("percent not doubled in StandardError:\n%s", out)
	}
}
