/**
 * `bun src/dev.ts` — one command to run the TUI against a local server.
 *
 * Ensures the worktree's dev server is up (reusing it when it already answers,
 * starting it otherwise), mints a session token for the TUI, and launches the
 * TUI attached to this terminal. The server is left running so the next run
 * starts instantly; the printed `kill` line is how to stop it.
 *
 *   vp run --filter @t3tools/tui dev
 */
import * as Effect from "effect/Effect";

import { run } from "./app.ts";

const packageDir = import.meta.dir;
const root = `${packageDir}/../../..`;
const stateDir = `${root}/.agenttunnel`;
const serverLog = `${stateDir}/dev-server.log`;
const pidFile = `${stateDir}/dev-server.pid`;

const sleep = (seconds: number) => Effect.runPromise(Effect.sleep(`${seconds} seconds`));

/** The port the dev runner picked, read from the banner it writes at startup. */
async function readPort(): Promise<string | null> {
  const log = Bun.file(serverLog);
  if (!(await log.exists())) return null;
  return /serverPort=(\d+)/u.exec(await log.text())?.[1] ?? null;
}

/** A TCP connect is enough to know whether the port already serves the API. */
async function listening(port: string): Promise<boolean> {
  try {
    await Bun.connect({
      hostname: "127.0.0.1",
      port: Number(port),
      socket: {
        data: () => {},
        open: (socket) => {
          socket.end();
        },
        error: () => {},
      },
    });
    return true;
  } catch {
    return false;
  }
}

/** Seed this worktree's state from the real install once, so there is data to read. */
async function seedState(): Promise<void> {
  const target = `${stateDir}/userdata/state.sqlite`;
  if (await Bun.file(target).exists()) return;
  const source = `${process.env.HOME ?? ""}/.agenttunnel/userdata/state.sqlite`;
  if (!(await Bun.file(source).exists())) return;
  Bun.spawnSync(["mkdir", "-p", `${stateDir}/userdata`]);
  Bun.spawnSync([
    "bun",
    "-e",
    `new (require("bun:sqlite").Database)(${JSON.stringify(source)}, { readonly: true }).run(${JSON.stringify(
      `VACUUM INTO '${target}'`,
    )})`,
  ]);
  process.stdout.write(`seeded ${target} from ${source}\n`);
}

async function ensureServer(): Promise<string> {
  const known = await readPort();
  if (known !== null && (await listening(known))) {
    process.stdout.write(`server already running on http://localhost:${known}\n`);
    return `http://localhost:${known}`;
  }

  await seedState();
  process.stdout.write("starting the dev server…\n");
  const server = Bun.spawn(["vp", "run", "dev:server"], {
    cwd: root,
    stdout: "pipe",
    stderr: "pipe",
    stdin: "ignore",
  });
  await Bun.write(pidFile, `${server.pid}\n`);

  let log = "";
  const collect = async (stream: ReadableStream<Uint8Array>) => {
    for await (const chunk of stream) log += new TextDecoder().decode(chunk);
  };
  void collect(server.stdout);
  void collect(server.stderr);

  for (let attempt = 0; attempt < 120; attempt += 1) {
    await sleep(1);
    const port = /serverPort=(\d+)/u.exec(log)?.[1];
    if (port !== undefined && (await listening(port))) {
      await Bun.write(serverLog, log);
      // The port opens a beat before auth finishes wiring, and a token minted
      // in that window is refused; let the server settle before handing over.
      await sleep(2);
      process.stdout.write(`server listening on http://localhost:${port}\n`);
      return `http://localhost:${port}`;
    }
  }
  await Bun.write(serverLog, log);
  throw new Error(`the dev server never came up; see ${serverLog}`);
}

/** A bearer session for the TUI: the WebSocket upgrade wants a ticket minted from one. */
function mintToken(): string {
  const issued = Bun.spawnSync(
    [
      "node",
      `${root}/apps/server/src/bin.ts`,
      "auth",
      "session",
      "issue",
      "--base-dir",
      stateDir,
      "--ttl",
      "8h",
      "--label",
      "tui",
      "--token-only",
    ],
    { cwd: root },
  );
  const token = `${issued.stdout.toString()}\n${issued.stderr.toString()}`
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.startsWith("eyJ"))
    .at(-1);
  if (token === undefined) {
    throw new Error(`could not mint a session token: ${issued.stderr.toString()}`);
  }
  return token;
}

const url = await ensureServer();
const token = mintToken();
process.stdout.write(
  `launching the TUI against ${url}\n  stop the server with: kill $(cat ${pidFile})\n\n`,
);

// Run the TUI in this process: a second `bun` child resolved Solid's server
// build, and this process already has the JSX runtime its bunfig sets up.
await run(["--url", url, "--token", token]);
