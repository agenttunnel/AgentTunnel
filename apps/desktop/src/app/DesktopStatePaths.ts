// @effect-diagnostics nodeBuiltinImport:off - pre-ready Electron startup adopts the home synchronously; see adoptLegacyDesktopBaseDir.
import * as NodeFS from "node:fs";
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
 * that read before {@link adoptLegacyDesktopBaseDir} runs — the Electron
 * pre-ready path, which has to pick a password store before the environment
 * layer exists — fall through this list so the rename does not look like a
 * fresh install.
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

export interface DesktopHomeFileSystem {
  readonly exists: (path: string) => boolean;
  readonly rename: (from: string, to: string) => void;
}

export interface DesktopBaseDirAdoption {
  readonly baseDir: string;
  /** Both names exist, so `.agenttunnel` won and the legacy home was left alone. */
  readonly legacyHomeLeftBehind: boolean;
}

/**
 * Adopt a pre-rename `~/.t3` home, synchronously.
 *
 * Desktop startup builds this before `@clerk/electron` creates its bridge, and
 * that bridge calls `protocol.registerSchemesAsPrivileged`, which Electron
 * rejects once it has emitted `ready`. Awaiting a filesystem round trip here
 * hands the event loop back, loses that race, and the app exits during startup
 * with no window — so this stays synchronous even though the server and dev
 * runner use the Effect version in `@t3tools/shared/devHome`.
 *
 * Same rule as that one: a new home wins, a legacy home is renamed once, and a
 * failed rename keeps the legacy home rather than strand the data behind a
 * fresh empty directory.
 */
export function adoptLegacyDesktopBaseDir(input: {
  readonly homeDirectory: string;
  readonly joinPath: JoinPath;
  readonly fileSystem?: DesktopHomeFileSystem;
}): DesktopBaseDirAdoption {
  const fileSystem = input.fileSystem ?? {
    exists: (path: string) => NodeFS.existsSync(path),
    rename: (from: string, to: string) => NodeFS.renameSync(from, to),
  };
  const agentTunnelHome = input.joinPath(input.homeDirectory, AGENTTUNNEL_HOME_DIR);
  const legacyHome = input.joinPath(input.homeDirectory, LEGACY_AGENTTUNNEL_HOME_DIR);
  if (fileSystem.exists(agentTunnelHome)) {
    return { baseDir: agentTunnelHome, legacyHomeLeftBehind: fileSystem.exists(legacyHome) };
  }
  if (!fileSystem.exists(legacyHome)) {
    return { baseDir: agentTunnelHome, legacyHomeLeftBehind: false };
  }
  try {
    fileSystem.rename(legacyHome, agentTunnelHome);
  } catch {
    return { baseDir: legacyHome, legacyHomeLeftBehind: false };
  }
  return { baseDir: agentTunnelHome, legacyHomeLeftBehind: false };
}
