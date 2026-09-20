import * as NodeServices from "@effect/platform-node/NodeServices";
import { describe, expect, it } from "@effect/vitest";
import { PiSettings } from "@t3tools/contracts";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Schema from "effect/Schema";
import * as Sink from "effect/Sink";
import * as Stream from "effect/Stream";
import { ChildProcessSpawner } from "effect/unstable/process";

import {
  checkPiProviderStatus,
  dedupePiListedModels,
  parsePiModelListTable,
  parsePiTokenCount,
  piListedModelsToServerModels,
} from "./PiProvider.ts";

const encoder = new TextEncoder();

// Recorded from `pi --list-models` (pi v0.78.0).
const PI_LIST_MODELS_FIXTURE = `provider     model                               context  max-out  thinking  images
google       gemini-2.0-flash                    1.0M     8.2K     no        yes
google       gemini-2.5-flash                    1.0M     65.5K    yes       yes
opencode-go  deepseek-v4-flash                   1M       384K     yes       no
opencode-go  glm-5                               202.8K   32.8K    yes       no
`;

function mockHandle(result: { stdout: string; stderr: string; code: number }) {
  return ChildProcessSpawner.makeHandle({
    pid: ChildProcessSpawner.ProcessId(1),
    exitCode: Effect.succeed(ChildProcessSpawner.ExitCode(result.code)),
    isRunning: Effect.succeed(false),
    kill: () => Effect.void,
    unref: Effect.succeed(Effect.void),
    stdin: Sink.drain,
    stdout: Stream.make(encoder.encode(result.stdout)),
    stderr: Stream.make(encoder.encode(result.stderr)),
    all: Stream.empty,
    getInputFd: () => Sink.drain,
    getOutputFd: () => Stream.empty,
  });
}

// pi v0.78.0 prints the model table to stderr under piped stdio; the default
// mock reproduces that, and the variants cover stdout or both.
function mockPiSpawnerLayer(listModelsStream: "stdout" | "stderr" | "both" = "stderr") {
  return Layer.succeed(
    ChildProcessSpawner.ChildProcessSpawner,
    ChildProcessSpawner.make((command) => {
      const cmd = command as unknown as { args: ReadonlyArray<string> };
      if (cmd.args.includes("--list-models")) {
        return Effect.succeed(
          mockHandle({
            stdout:
              listModelsStream === "stdout" || listModelsStream === "both"
                ? PI_LIST_MODELS_FIXTURE
                : "",
            stderr:
              listModelsStream === "stderr" || listModelsStream === "both"
                ? PI_LIST_MODELS_FIXTURE
                : "",
            code: 0,
          }),
        );
      }
      if (cmd.args.includes("--version")) {
        return Effect.succeed(mockHandle({ stdout: "0.78.0", stderr: "", code: 0 }));
      }
      return Effect.succeed(mockHandle({ stdout: "", stderr: "unknown args", code: 1 }));
    }),
  );
}

const decodePiSettings = Schema.decodeSync(PiSettings);
describe("parsePiTokenCount", () => {
  it("converts abbreviated and bare token counts to integers", () => {
    expect(parsePiTokenCount("1.0M")).toBe(1_000_000);
    expect(parsePiTokenCount("1M")).toBe(1_000_000);
    expect(parsePiTokenCount("65.5K")).toBe(65_500);
    expect(parsePiTokenCount("384K")).toBe(384_000);
    expect(parsePiTokenCount("202.8K")).toBe(202_800);
    expect(parsePiTokenCount("1200")).toBe(1_200);
  });

  it("returns undefined for unparseable cells", () => {
    expect(parsePiTokenCount("")).toBeUndefined();
    expect(parsePiTokenCount("n/a")).toBeUndefined();
  });
});

describe("parsePiModelListTable", () => {
  it("skips the header and parses the documented columns", () => {
    const rows = parsePiModelListTable(PI_LIST_MODELS_FIXTURE);
    expect(rows).toHaveLength(4);
    expect(rows[0]).toEqual({
      provider: "google",
      model: "gemini-2.0-flash",
      contextWindowTokens: 1_000_000,
      maxOutputTokens: 8_200,
      thinking: false,
      images: true,
    });
    expect(rows[2]).toEqual({
      provider: "opencode-go",
      model: "deepseek-v4-flash",
      contextWindowTokens: 1_000_000,
      maxOutputTokens: 384_000,
      thinking: true,
      images: false,
    });
  });

  it("drops malformed rows", () => {
    const rows = parsePiModelListTable(
      ["provider  model  context  max-out  thinking  images", "google  only-three  columns"].join(
        "\n",
      ),
    );
    expect(rows).toEqual([]);
  });
});

describe("piListedModelsToServerModels", () => {
  it("builds provider/model slugs with the provider as sub-provider", () => {
    const models = piListedModelsToServerModels(parsePiModelListTable(PI_LIST_MODELS_FIXTURE));
    expect(models.map((model) => model.slug)).toEqual([
      "google/gemini-2.0-flash",
      "google/gemini-2.5-flash",
      "opencode-go/deepseek-v4-flash",
      "opencode-go/glm-5",
    ]);
    expect(models[1]).toMatchObject({
      name: "gemini-2.5-flash",
      subProvider: "google",
      isCustom: false,
    });
  });
});

describe("dedupePiListedModels", () => {
  it("keeps the first occurrence of each provider/model row", () => {
    const rows = parsePiModelListTable(`${PI_LIST_MODELS_FIXTURE}${PI_LIST_MODELS_FIXTURE}`);
    expect(rows).toHaveLength(8);
    expect(dedupePiListedModels(rows)).toHaveLength(4);
  });
});

describe("checkPiProviderStatus", () => {
  it.layer(NodeServices.layer)("pi provider snapshot", (it) => {
    it.effect("lists models from stderr and reports ready without requiring auth", () =>
      Effect.gen(function* () {
        const snapshot = yield* checkPiProviderStatus(
          decodePiSettings({ enabled: true, binaryPath: "pi" }),
          process.cwd(),
        );

        expect(snapshot.installed).toBe(true);
        expect(snapshot.version).toBe("0.78.0");
        expect(snapshot.status).toBe("ready");
        expect(snapshot.auth.status).toBe("unknown");
        expect(snapshot.models.map((model) => model.slug)).toEqual([
          "google/gemini-2.0-flash",
          "google/gemini-2.5-flash",
          "opencode-go/deepseek-v4-flash",
          "opencode-go/glm-5",
        ]);
      }).pipe(Effect.provide(mockPiSpawnerLayer("stderr"))),
    );

    it.effect("lists models when the table arrives on stdout instead", () =>
      Effect.gen(function* () {
        const snapshot = yield* checkPiProviderStatus(
          decodePiSettings({ enabled: true, binaryPath: "pi" }),
          process.cwd(),
        );

        expect(snapshot.status).toBe("ready");
        expect(snapshot.models).toHaveLength(4);
      }).pipe(Effect.provide(mockPiSpawnerLayer("stdout"))),
    );

    it.effect("does not duplicate models when the table arrives on both streams", () =>
      Effect.gen(function* () {
        const snapshot = yield* checkPiProviderStatus(
          decodePiSettings({ enabled: true, binaryPath: "pi" }),
          process.cwd(),
        );

        expect(snapshot.status).toBe("ready");
        expect(snapshot.models).toHaveLength(4);
      }).pipe(Effect.provide(mockPiSpawnerLayer("both"))),
    );

    it.effect("stays disabled when settings disable the provider", () =>
      Effect.gen(function* () {
        const snapshot = yield* checkPiProviderStatus(
          decodePiSettings({ enabled: false, binaryPath: "pi" }),
          process.cwd(),
        );

        expect(snapshot.enabled).toBe(false);
        expect(snapshot.status).toBe("disabled");
        expect(snapshot.installed).toBe(false);
        expect(snapshot.message).toContain("disabled");
      }).pipe(Effect.provide(mockPiSpawnerLayer())),
    );
  });
});
