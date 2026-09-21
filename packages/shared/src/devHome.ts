/**
 * Where development state lives, and how to keep it away from the shared
 * `~/.agenttunnel` that a user's installed AgentTunnel runs against.
 *
 * A linked git worktree gets its own (gitignored) `.agenttunnel`: feature work
 * in a throwaway branch must not share a database with the real app, and an
 * ambient `T3CODE_HOME` counts as an explicit base dir — flipping the state
 * directory from `<base>/dev` to `<base>/userdata`, the live production
 * database.
 */

import * as Effect from "effect/Effect";
import * as FileSystem from "effect/FileSystem";
import * as Option from "effect/Option";
import * as Path from "effect/Path";

/** The internal AgentTunnel home directory name inside a home/repo directory. */
export const AGENTTUNNEL_HOME_DIR = ".agenttunnel";
/** The pre-rename home name; migrated to `AGENTTUNNEL_HOME_DIR` once at startup. */
const LEGACY_AGENTTUNNEL_HOME_DIR = ".t3";

/**
 * One-time rename of a legacy `.t3` home directory. A user who upgraded from
 * the pre-rename builds keeps their data at `~/.t3` (or `<worktree>/.t3`), so
 * the first run after the rename moves it to the new name before anything is
 * created there. Idempotent:
 *
 * - new home already exists → leave both alone, use the new home;
 * - legacy home missing → nothing to move, use the new home;
 * - legacy home present → rename it, and on failure keep using the legacy
 *   home rather than strand the data behind a fresh empty directory (the next
 *   start retries).
 */
export const resolveAgentTunnelHomeDir = Effect.fn("resolveAgentTunnelHomeDir")(function* (
  parentDirectory: string,
) {
  const fileSystem = yield* FileSystem.FileSystem;
  const path = yield* Path.Path;
  const agentTunnelHome = path.join(parentDirectory, AGENTTUNNEL_HOME_DIR);
  const legacyHome = path.join(parentDirectory, LEGACY_AGENTTUNNEL_HOME_DIR);
  const agentTunnelExists = yield* fileSystem
    .exists(agentTunnelHome)
    .pipe(Effect.orElseSucceed(() => false));
  if (agentTunnelExists) {
    return agentTunnelHome;
  }
  const legacyExists = yield* fileSystem.exists(legacyHome).pipe(Effect.orElseSucceed(() => false));
  if (!legacyExists) {
    return agentTunnelHome;
  }
  yield* fileSystem.rename(legacyHome, agentTunnelHome).pipe(
    Effect.tap(() => Effect.logInfo("Migrated AgentTunnel home from .t3 to .agenttunnel.")),
    Effect.catch((cause) =>
      Effect.logWarning("Could not migrate the legacy .t3 home; keeping it in place.", {
        cause,
        legacyHome,
        agentTunnelHome,
      }),
    ),
  );
  const renamed = yield* fileSystem.exists(agentTunnelHome).pipe(Effect.orElseSucceed(() => false));
  if (renamed) {
    return agentTunnelHome;
  }
  return yield* fileSystem.exists(legacyHome).pipe(
    Effect.map((stillThere) => (stillThere ? legacyHome : agentTunnelHome)),
    Effect.orElseSucceed(() => agentTunnelHome),
  );
});

/**
 * A `.git` file points at the real git directory. A linked worktree's lives at
 * `<common-dir>/worktrees/<name>`; a submodule's at
 * `<super-git-dir>/modules/<name>`. Both are files, so the pointer — not the
 * file-vs-directory distinction alone — is what identifies a worktree.
 *
 * The common dir is not necessarily named `.git`: a worktree of a bare repo
 * points at `<repo>.git/worktrees/<name>`, and `$GIT_COMMON_DIR` can be
 * anything. So match on the `worktrees/<name>` tail, which git always uses,
 * rather than on the name of the directory containing it.
 */
const pointsAtLinkedWorktree = (gitFileContents: string, path: Path.Path): boolean => {
  const gitdir = gitFileContents
    .split(/\r?\n/)
    .map((line) => line.trim())
    .find((line) => line.startsWith("gitdir:"))
    ?.slice("gitdir:".length)
    .trim();
  if (gitdir === undefined || gitdir.length === 0) {
    return false;
  }
  // Compare as path segments so a directory merely named `…worktrees…` cannot
  // match as a substring. Trailing separators normalize away first.
  const segments = path
    .normalize(gitdir.replaceAll("\\", "/"))
    .split(/[/\\]/)
    .filter((segment) => segment.length > 0);
  // `<common-dir>/worktrees/<name>`: `worktrees` is the penultimate segment,
  // and something must precede it. This excludes `<git-dir>/modules/<name>`.
  return segments.length >= 3 && segments.at(-2) === "worktrees";
};

/**
 * The path of the linked git worktree containing `cwd`, or undefined when
 * `cwd` is not inside one. Git marks a linked worktree by making `.git` a file
 * whose `gitdir:` points into the repository's `.git/worktrees`.
 *
 * Walks up to the repository root, so running from a subdirectory resolves the
 * same worktree as running from the top.
 */
export const resolveGitWorktreePath = (
  cwd: string,
): Effect.Effect<string | undefined, never, FileSystem.FileSystem | Path.Path> =>
  Effect.gen(function* () {
    const fileSystem = yield* FileSystem.FileSystem;
    const path = yield* Path.Path;

    let directory = path.resolve(cwd);
    for (;;) {
      const gitPath = path.join(directory, ".git");
      const info = yield* fileSystem.stat(gitPath).pipe(Effect.option);
      if (Option.isSome(info)) {
        // A directory means the main checkout. Stop either way: nesting one
        // repository inside another does not make the outer one this root.
        if (info.value.type !== "File") {
          return undefined;
        }
        // A submodule also has a `.git` file, but it is not a worktree of this
        // repository and gets no worktree-local home.
        const contents = yield* fileSystem
          .readFileString(gitPath)
          .pipe(Effect.orElseSucceed(() => ""));
        return pointsAtLinkedWorktree(contents, path) ? directory : undefined;
      }
      const parent = path.dirname(directory);
      if (parent === directory) {
        return undefined;
      }
      directory = parent;
    }
  });

/**
 * The worktree-local data directory for `cwd`, or undefined outside a linked
 * worktree. Deliberately does not require the directory to exist yet: falling
 * back because it is missing would send callers at the shared home. When a
 * pre-rename `.t3` exists it is migrated once to `.agenttunnel`.
 */
export const resolveWorktreeT3Home = (
  cwd: string,
): Effect.Effect<string | undefined, never, FileSystem.FileSystem | Path.Path> =>
  Effect.gen(function* () {
    const worktreePath = yield* resolveGitWorktreePath(cwd);
    if (worktreePath === undefined) {
      return undefined;
    }
    return yield* resolveAgentTunnelHomeDir(worktreePath);
  });
