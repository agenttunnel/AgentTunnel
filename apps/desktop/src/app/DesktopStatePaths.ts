import * as Option from "effect/Option";

import { AGENTTUNNEL_HOME_DIR, LEGACY_AGENTTUNNEL_HOME_DIR } from "@t3tools/shared/devHome";

export type JoinPath = (first: string, ...segments: string[]) => string;

function normalizeConfiguredBaseDir(t3Home: Option.Option<string>): Option.Option<string> {
  if (Option.isNone(t3Home)) {
    return Option.none();
  }
  const trimmed = t3Home.value.trim();
  return trimmed.length > 0 ? Option.some(trimmed) : Option.none();
}

export function resolveDesktopBaseDir(input: {
  readonly homeDirectory: string;
  readonly joinPath: JoinPath;
  readonly t3Home: Option.Option<string>;
}): string {
  return Option.getOrElse(normalizeConfiguredBaseDir(input.t3Home), () =>
    input.joinPath(input.homeDirectory, AGENTTUNNEL_HOME_DIR),
  );
}

export function resolveDesktopStateDir(input: {
  readonly baseDir: string;
  readonly isDevelopment: boolean;
  readonly joinPath: JoinPath;
  readonly t3Home: Option.Option<string>;
}): string {
  const useDevSubdir =
    input.isDevelopment && Option.isNone(normalizeConfiguredBaseDir(input.t3Home));
  return input.joinPath(input.baseDir, useDevSubdir ? "dev" : "userdata");
}

/**
 * Every state directory an upgraded install may still be reading from, most
 * current first. Only the implicit home has a pre-rename alias: an explicit
 * `T3CODE_HOME` points wherever the user said, and never at `~/.t3`. Callers
 * that read before {@link resolveAgentTunnelHomeDir} migrates that home (the
 * Electron startup path) fall through this list so the rename does not look
 * like a fresh install.
 */
export function resolveDesktopStateDirCandidates(input: {
  readonly homeDirectory: string;
  readonly baseDir: string;
  readonly isDevelopment: boolean;
  readonly joinPath: JoinPath;
  readonly t3Home: Option.Option<string>;
}): readonly string[] {
  const stateDir = resolveDesktopStateDir(input);
  // Matches `resolveDesktopStateDir`'s own notion of "configured": a
  // whitespace-only `T3CODE_HOME` still resolves to the implicit home, so that
  // home's alias applies to it too.
  if (Option.isSome(normalizeConfiguredBaseDir(input.t3Home))) {
    return [stateDir];
  }
  return [
    stateDir,
    resolveDesktopStateDir({
      ...input,
      baseDir: input.joinPath(input.homeDirectory, LEGACY_AGENTTUNNEL_HOME_DIR),
      t3Home: Option.none(),
    }),
  ];
}
