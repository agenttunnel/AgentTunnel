package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// piSteeringExtension is a Pi extension that hands a solo agent the steering
// pending on its thread after each tool result. It asks the daemon through the
// same `"$AT_TOOL" tool steering` command Claude's hook runs, and reads the
// Claude-shaped hook envelope it prints, so the daemon has one answer format.
// A daemon that cannot be reached adds nothing: a steer waits for the next pass.
const piSteeringExtension = `import { execFile } from "node:child_process";

export default function (pi) {
  pi.on("tool_result", async (event) => {
    const context = await pendingSteering();
    if (!context) return undefined;
    return { content: [...event.content, { type: "text", text: "\n\n" + context }] };
  });
}

` + steeringFetchJS

// openCodeSteeringPlugin is an OpenCode plugin with the same contract as the Pi
// extension. It drains steering only after a completed call: a failed call has
// no result to append to, so its steering waits for the next completed call.
const openCodeSteeringPlugin = `import { execFile } from "node:child_process";

export default {
  id: "agenttunnel-steering",
  async setup(ctx) {
    if (typeof ctx.tool?.hook !== "function") return;
    await ctx.tool.hook("execute.after", async (event) => {
      if (event.status !== "completed") return;
      const context = await pendingSteering();
      if (!context) return;
      const prev = event.result ?? {};
      event.result = { ...prev, output: String(prev.output ?? "") + "\n\n" + context };
    });
  },
};

` + steeringFetchJS

// commandCodeSteeringMod is a Command Code mod that appends the steering pending
// on its thread to each tool result. afterToolCall also runs for a failed call,
// where isError says so, so steering lands after a failure too.
const commandCodeSteeringMod = `import { execFile } from "node:child_process";

export default function (cmd) {
  cmd.hooks({
    afterToolCall: async () => {
      const context = await pendingSteering();
      return context ? { additionalContext: context } : undefined;
    },
  });
}

` + steeringFetchJS

// steeringFetchJS asks the daemon for the steering pending on this thread and
// returns the context block, or "" when none is pending or the daemon is out of
// reach. Shared by the JS glue files.
const steeringFetchJS = `function pendingSteering() {
  const tool = process.env.AT_TOOL;
  if (!tool) return Promise.resolve("");
  return new Promise((resolve) => {
    execFile(tool, ["tool", "steering"], { timeout: 10000 }, (err, stdout) => {
      if (err) return resolve("");
      try {
        resolve(JSON.parse(stdout).hookSpecificOutput?.additionalContext ?? "");
      } catch {
        resolve("");
      }
    });
  });
}
`

// writeSteeringGlue writes a harness glue file under the OS temp directory and
// returns its path. The file sits in a directory named for a hash of its body,
// so a body is written once and a concurrent run never reads a half-written
// file. Harnesses that load a plugin directory get the directory from the path.
func writeSteeringGlue(name, file, body string) (string, error) {
	sum := sha256.Sum256([]byte(body))
	dir := filepath.Join(os.TempDir(), "agenttunnel-glue", name+"-"+hex.EncodeToString(sum[:6]))
	path := filepath.Join(dir, file)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, file+"-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
