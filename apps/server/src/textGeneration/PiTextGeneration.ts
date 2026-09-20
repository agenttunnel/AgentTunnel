/**
 * PiTextGeneration — commit / change-request / branch / thread-title text
 * generation backed by a one-shot `pi --mode json -p <prompt>` invocation.
 *
 * Mirrors the OpenCode implementation's shape (prompt builders, structured JSON
 * decoding, sanitizers) but uses pi's non-interactive JSON event stream instead
 * of a long-lived server. Each generation is a fresh child process, so there is
 * no session or idle lifecycle to manage.
 *
 * @module textGeneration/PiTextGeneration
 */
import type { ChatAttachment, ModelSelection, PiSettings } from "@t3tools/contracts";
import { TextGenerationError } from "@t3tools/contracts";
import { sanitizeBranchFragment, sanitizeFeatureBranchName } from "@t3tools/shared/git";
import { getModelSelectionStringOptionValue } from "@t3tools/shared/model";
import { extractJsonObject } from "@t3tools/shared/schemaJson";
import * as Effect from "effect/Effect";
import * as Schema from "effect/Schema";
import { ChildProcessSpawner } from "effect/unstable/process";

import { parsePiModelSlug } from "../provider/Layers/PiAdapter.ts";
import { piRuntimeErrorDetail, runPiJsonOneShot } from "../provider/piRuntime.ts";
import * as TextGeneration from "./TextGeneration.ts";
import {
  buildBranchNamePrompt,
  buildCommitMessagePrompt,
  buildPrContentPrompt,
  buildThreadTitlePrompt,
} from "./TextGenerationPrompts.ts";
import {
  sanitizeCommitSubject,
  sanitizePrTitle,
  sanitizeThreadTitle,
} from "./TextGenerationUtils.ts";

const PiTextGenerationOperation = Schema.Literals([
  "generateCommitMessage",
  "generatePrContent",
  "generateBranchName",
  "generateThreadTitle",
]);

type PiTextGenerationOperation = typeof PiTextGenerationOperation.Type;

export const makePiTextGeneration = Effect.fn("makePiTextGeneration")(function* (
  piSettings: PiSettings,
  environment: NodeJS.ProcessEnv = process.env,
) {
  const spawner = yield* ChildProcessSpawner.ChildProcessSpawner;

  const runPiJson = Effect.fn("runPiJson")(function* <S extends Schema.Top>(input: {
    readonly operation: PiTextGenerationOperation;
    readonly cwd: string;
    readonly prompt: string;
    readonly outputSchemaJson: S;
    readonly modelSelection: ModelSelection;
    readonly attachments?: ReadonlyArray<ChatAttachment> | undefined;
  }) {
    const parsedModel = parsePiModelSlug(input.modelSelection.model);
    if (parsedModel === undefined) {
      return yield* new TextGenerationError({
        operation: input.operation,
        detail: "Pi model selection must use the 'provider/model' format.",
      });
    }

    const args: Array<string> = [
      "--model",
      `${parsedModel.provider}/${parsedModel.modelId}`,
      // Text generation is a throwaway one-shot; don't persist a session.
      "--no-session",
    ];
    const thinkingLevel =
      getModelSelectionStringOptionValue(input.modelSelection, "thinkingLevel") ??
      getModelSelectionStringOptionValue(input.modelSelection, "thinking") ??
      getModelSelectionStringOptionValue(input.modelSelection, "effort");
    if (thinkingLevel !== undefined && thinkingLevel.trim().length > 0) {
      args.push("--thinking", thinkingLevel.trim());
    }

    const result = yield* runPiJsonOneShot({
      binaryPath: piSettings.binaryPath,
      prompt: input.prompt,
      args,
      cwd: input.cwd,
      environment,
    }).pipe(
      Effect.provideService(ChildProcessSpawner.ChildProcessSpawner, spawner),
      Effect.mapError(
        (cause) =>
          new TextGenerationError({
            operation: input.operation,
            detail: piRuntimeErrorDetail(cause),
            cause,
          }),
      ),
    );

    if (result.code !== 0) {
      return yield* new TextGenerationError({
        operation: input.operation,
        // Keep the surfaced detail bounded rather than forwarding raw stderr.
        detail: `Pi exited with code ${result.code} (${result.stderr.trim().length} bytes on stderr).`,
      });
    }

    const rawOutput = result.text?.trim() ?? "";
    if (rawOutput.length === 0) {
      return yield* new TextGenerationError({
        operation: input.operation,
        detail: "Pi returned empty output.",
      });
    }

    const decodeOutput = Schema.decodeEffect(Schema.fromJsonString(input.outputSchemaJson));
    return yield* decodeOutput(extractJsonObject(rawOutput)).pipe(
      Effect.catchTags({
        SchemaError: (cause) =>
          Effect.fail(
            new TextGenerationError({
              operation: input.operation,
              detail: "Pi returned invalid structured output.",
              cause,
            }),
          ),
      }),
    );
  });

  const generateCommitMessage: TextGeneration.TextGeneration["Service"]["generateCommitMessage"] =
    Effect.fn("PiTextGeneration.generateCommitMessage")(function* (input) {
      const { prompt, outputSchema } = buildCommitMessagePrompt({
        branch: input.branch,
        stagedSummary: input.stagedSummary,
        stagedPatch: input.stagedPatch,
        includeBranch: input.includeBranch === true,
        policy: input.policy,
      });
      const generated = yield* runPiJson({
        operation: "generateCommitMessage",
        cwd: input.cwd,
        prompt,
        outputSchemaJson: outputSchema,
        modelSelection: input.modelSelection,
      });

      return {
        subject: sanitizeCommitSubject(generated.subject),
        body: generated.body.trim(),
        ...("branch" in generated && typeof generated.branch === "string"
          ? { branch: sanitizeFeatureBranchName(generated.branch) }
          : {}),
      };
    });

  const generatePrContent: TextGeneration.TextGeneration["Service"]["generatePrContent"] =
    Effect.fn("PiTextGeneration.generatePrContent")(function* (input) {
      const { prompt, outputSchema } = buildPrContentPrompt({
        baseBranch: input.baseBranch,
        headBranch: input.headBranch,
        commitSummary: input.commitSummary,
        diffSummary: input.diffSummary,
        diffPatch: input.diffPatch,
        policy: input.policy,
        changeRequestTemplate: input.changeRequestTemplate,
      });
      const generated = yield* runPiJson({
        operation: "generatePrContent",
        cwd: input.cwd,
        prompt,
        outputSchemaJson: outputSchema,
        modelSelection: input.modelSelection,
      });

      return {
        title: sanitizePrTitle(generated.title),
        body: generated.body.trim(),
      };
    });

  const generateBranchName: TextGeneration.TextGeneration["Service"]["generateBranchName"] =
    Effect.fn("PiTextGeneration.generateBranchName")(function* (input) {
      const { prompt, outputSchema } = buildBranchNamePrompt({
        message: input.message,
        attachments: input.attachments,
      });
      const generated = yield* runPiJson({
        operation: "generateBranchName",
        cwd: input.cwd,
        prompt,
        outputSchemaJson: outputSchema,
        modelSelection: input.modelSelection,
        attachments: input.attachments,
      });

      return {
        branch: sanitizeBranchFragment(generated.branch),
      };
    });

  const generateThreadTitle: TextGeneration.TextGeneration["Service"]["generateThreadTitle"] =
    Effect.fn("PiTextGeneration.generateThreadTitle")(function* (input) {
      const { prompt, outputSchema } = buildThreadTitlePrompt({
        message: input.message,
        previousTitle: input.previousTitle,
        attachments: input.attachments,
      });
      const generated = yield* runPiJson({
        operation: "generateThreadTitle",
        cwd: input.cwd,
        prompt,
        outputSchemaJson: outputSchema,
        modelSelection: input.modelSelection,
        attachments: input.attachments,
      });

      return {
        title: sanitizeThreadTitle(generated.title),
      };
    });

  return {
    generateCommitMessage,
    generatePrContent,
    generateBranchName,
    generateThreadTitle,
  } satisfies TextGeneration.TextGeneration["Service"];
});
