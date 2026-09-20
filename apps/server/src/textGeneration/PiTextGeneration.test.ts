import * as NodeServices from "@effect/platform-node/NodeServices";
import { TextGenerationError, PiSettings, ProviderInstanceId } from "@t3tools/contracts";
import { it } from "@effect/vitest";
import * as Effect from "effect/Effect";
import * as FileSystem from "effect/FileSystem";
import * as Layer from "effect/Layer";
import * as Path from "effect/Path";
import * as Schema from "effect/Schema";
import * as Sink from "effect/Sink";
import * as Stream from "effect/Stream";
import { ChildProcessSpawner } from "effect/unstable/process";
import { beforeEach, expect } from "vite-plus/test";

import * as ServerConfig from "../config.ts";
import * as PiTextGeneration from "./PiTextGeneration.ts";

const encoder = new TextEncoder();

const spawnerState = {
  calls: [] as Array<ReadonlyArray<string>>,
  stdout: "",
  stderr: "",
  code: 0,
  reset() {
    this.calls.length = 0;
    this.stdout = "";
    this.stderr = "";
    this.code = 0;
  },
};

function mockHandle() {
  return ChildProcessSpawner.makeHandle({
    pid: ChildProcessSpawner.ProcessId(1),
    exitCode: Effect.succeed(ChildProcessSpawner.ExitCode(spawnerState.code)),
    isRunning: Effect.succeed(false),
    kill: () => Effect.void,
    unref: Effect.succeed(Effect.void),
    stdin: Sink.drain,
    stdout: Stream.make(encoder.encode(spawnerState.stdout)),
    stderr: Stream.make(encoder.encode(spawnerState.stderr)),
    all: Stream.empty,
    getInputFd: () => Sink.drain,
    getOutputFd: () => Stream.empty,
  });
}

const MockSpawnerLayer = Layer.succeed(
  ChildProcessSpawner.ChildProcessSpawner,
  ChildProcessSpawner.make((command) => {
    const cmd = command as unknown as { args: ReadonlyArray<string> };
    spawnerState.calls.push(cmd.args);
    return Effect.succeed(mockHandle());
  }),
);

const PiTextGenerationTestLayer = ServerConfig.ServerConfig.layerTest(process.cwd(), {
  prefix: "t3code-pi-text-generation-test-",
}).pipe(Layer.provideMerge(Layer.mergeAll(NodeServices.layer, MockSpawnerLayer)));

function assistantOutput(text: string): string {
  const escaped = text.replaceAll("\\", "\\\\").replaceAll('"', '\\"');
  return (
    '{"type":"session","sessionId":"test-session"}\n' +
    `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"${escaped}"}]}}\n`
  );
}

const decodePiSettings = Schema.decodeSync(PiSettings);
const PI_SETTINGS = decodePiSettings({ binaryPath: "pi" });
const DEFAULT_MODEL_SELECTION = {
  instanceId: ProviderInstanceId.make("pi"),
  model: "google/gemini-3-pro-preview",
};

beforeEach(() => {
  spawnerState.reset();
});

it.layer(PiTextGenerationTestLayer)("PiTextGeneration", (it) => {
  it.effect("passes the provider/model selection to a one-shot pi invocation", () =>
    Effect.gen(function* () {
      spawnerState.stdout = assistantOutput(
        '{"subject":"Add pi text generation","body":"Use one-shot pi."}',
      );
      const textGeneration = yield* PiTextGeneration.makePiTextGeneration(PI_SETTINGS);

      const result = yield* textGeneration.generateCommitMessage({
        cwd: process.cwd(),
        branch: "feature/pi-textgen",
        stagedSummary: "M README.md",
        stagedPatch: "diff --git a/README.md b/README.md",
        modelSelection: DEFAULT_MODEL_SELECTION,
      });

      expect(result).toEqual({
        subject: "Add pi text generation",
        body: "Use one-shot pi.",
      });
      expect(spawnerState.calls[0]?.slice(0, 3)).toEqual(["--mode", "json", "-p"]);
      expect(spawnerState.calls[0]).toEqual(
        expect.arrayContaining(["--model", "google/gemini-3-pro-preview"]),
      );
    }),
  );

  it.effect("forwards a thinking level selection as --thinking", () =>
    Effect.gen(function* () {
      spawnerState.stdout = assistantOutput('{"branch":"add-pi-textgen"}');
      const textGeneration = yield* PiTextGeneration.makePiTextGeneration(PI_SETTINGS);

      yield* textGeneration.generateBranchName({
        cwd: process.cwd(),
        message: "Add pi text generation",
        modelSelection: {
          ...DEFAULT_MODEL_SELECTION,
          options: [{ id: "thinkingLevel", value: "high" }],
        },
      });

      expect(spawnerState.calls[0]).toEqual(expect.arrayContaining(["--thinking", "high"]));
    }),
  );

  it.effect("forwards image attachments as @path arguments", () =>
    Effect.gen(function* () {
      spawnerState.stdout = assistantOutput('{"branch":"fix/ui-regression"}');
      const fs = yield* FileSystem.FileSystem;
      const path = yield* Path.Path;
      const { attachmentsDir } = yield* ServerConfig.ServerConfig;
      const attachmentId = "thread-branch-image-attachment";
      const imagePath = path.join(attachmentsDir, `${attachmentId}.png`);
      yield* fs.makeDirectory(attachmentsDir, { recursive: true });
      yield* fs.writeFile(imagePath, new Uint8Array([1, 2, 3]));

      const textGeneration = yield* PiTextGeneration.makePiTextGeneration(PI_SETTINGS);
      yield* textGeneration.generateBranchName({
        cwd: process.cwd(),
        message: "Fix layout bug from screenshot.",
        modelSelection: DEFAULT_MODEL_SELECTION,
        attachments: [
          {
            type: "image",
            id: attachmentId,
            name: "bug.png",
            mimeType: "image/png",
            sizeBytes: 3,
          },
        ],
      });

      expect(spawnerState.calls[0]).toEqual(expect.arrayContaining([`@${imagePath}`]));
    }),
  );

  it.effect("skips non-image and missing attachments", () =>
    Effect.gen(function* () {
      spawnerState.stdout = assistantOutput('{"branch":"add-pi-textgen"}');
      const textGeneration = yield* PiTextGeneration.makePiTextGeneration(PI_SETTINGS);

      yield* textGeneration.generateBranchName({
        cwd: process.cwd(),
        message: "Add pi text generation",
        modelSelection: DEFAULT_MODEL_SELECTION,
        attachments: [
          {
            type: "file",
            id: "thread-file-attachment",
            name: "notes.txt",
            mimeType: "text/plain",
            sizeBytes: 5,
          },
          {
            type: "image",
            id: "thread-missing-image-attachment",
            name: "missing.png",
            mimeType: "image/png",
            sizeBytes: 5,
          },
        ],
      });

      const args = spawnerState.calls[0] ?? [];
      expect(args.some((arg) => arg.startsWith("@"))).toBe(false);
    }),
  );

  it.effect("rejects a model selection without a provider prefix", () =>
    Effect.gen(function* () {
      const textGeneration = yield* PiTextGeneration.makePiTextGeneration(PI_SETTINGS);

      const error = yield* textGeneration
        .generateCommitMessage({
          cwd: process.cwd(),
          branch: null,
          stagedSummary: "M README.md",
          stagedPatch: "diff --git a/README.md b/README.md",
          modelSelection: { ...DEFAULT_MODEL_SELECTION, model: "gemini-3-pro-preview" },
        })
        .pipe(Effect.flip);

      expect(error).toBeInstanceOf(TextGenerationError);
      expect(error.detail).toContain("provider/model");
    }),
  );

  it.effect("surfaces a non-zero pi exit as a typed error", () =>
    Effect.gen(function* () {
      spawnerState.code = 1;
      spawnerState.stderr = "not authenticated";
      const textGeneration = yield* PiTextGeneration.makePiTextGeneration(PI_SETTINGS);

      const error = yield* textGeneration
        .generateCommitMessage({
          cwd: process.cwd(),
          branch: null,
          stagedSummary: "M README.md",
          stagedPatch: "diff --git a/README.md b/README.md",
          modelSelection: DEFAULT_MODEL_SELECTION,
        })
        .pipe(Effect.flip);

      expect(error.detail).toContain("exited with code 1");
      expect(error.detail).not.toContain("not authenticated");
    }),
  );

  it.effect("reports empty output instead of manufacturing a result", () =>
    Effect.gen(function* () {
      spawnerState.stdout = assistantOutput("   ");
      const textGeneration = yield* PiTextGeneration.makePiTextGeneration(PI_SETTINGS);

      const error = yield* textGeneration
        .generateCommitMessage({
          cwd: process.cwd(),
          branch: null,
          stagedSummary: "M README.md",
          stagedPatch: "diff --git a/README.md b/README.md",
          modelSelection: DEFAULT_MODEL_SELECTION,
        })
        .pipe(Effect.flip);

      expect(error).toBeInstanceOf(TextGenerationError);
      expect(error.detail).toBe("Pi returned empty output.");
    }),
  );

  it.effect("reports invalid structured output", () =>
    Effect.gen(function* () {
      spawnerState.stdout = assistantOutput('{"unexpected":true}');
      const textGeneration = yield* PiTextGeneration.makePiTextGeneration(PI_SETTINGS);

      const error = yield* textGeneration
        .generateCommitMessage({
          cwd: process.cwd(),
          branch: null,
          stagedSummary: "M README.md",
          stagedPatch: "diff --git a/README.md b/README.md",
          modelSelection: DEFAULT_MODEL_SELECTION,
        })
        .pipe(Effect.flip);

      expect(error.detail).toBe("Pi returned invalid structured output.");
    }),
  );
});
