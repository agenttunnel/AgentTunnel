#!/usr/bin/env node
import React from 'react';
import { render } from 'ink';
import { App } from './ui/App.js';

interface ParsedArgs {
  workspace: string;
  autoApprove: boolean;
  model: string | undefined;
  help: boolean;
}

function parseArgs(argv: string[]): ParsedArgs {
  const out: ParsedArgs = {
    workspace: process.cwd(),
    autoApprove: false,
    model: undefined,
    help: false,
  };

  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '-h' || a === '--help') out.help = true;
    else if (a === '-w' || a === '--workspace') out.workspace = argv[++i] ?? out.workspace;
    else if (a === '-m' || a === '--model') out.model = argv[++i];
    else if (a === '--yes' || a === '--auto-approve') out.autoApprove = true;
    else if (a === '--') {
      // ignore remainder
    } else if (a && a.startsWith('-')) {
      console.error(`Unknown option: ${a}`);
      process.exit(2);
    }
  }
  return out;
}

function printHelp(): void {
  console.log(`Usage: agenttunnel [options]

A full-screen terminal UI wrapper for opencode AI coding agents with
multi-agent orchestration.

Options:
  -w, --workspace <path>   Run inside the given directory (default: cwd)
  -m, --model <name>       Default model for all roles
      --yes                Auto-approve all tool calls
  -h, --help               Show this help

Keys (in-app):
  Enter         Submit task
  ↑ / ↓         History (or navigate hints)
  PgUp / PgDn   Scroll output
  Tab           Autocomplete hint
  Esc           Leave config view
  Ctrl+C        Cancel running task (or exit when idle)
`);
}

function main(): void {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    printHelp();
    return;
  }

  if (!process.stdin.isTTY || !process.stdout.isTTY) {
    console.error('agenttunnel requires an interactive TTY.');
    process.exit(2);
  }

  const onSignal = (sig: NodeJS.Signals): void => {
    process.stderr.write(`\n[agenttunnel] received ${sig}, shutting down\n`);
    process.exit(130);
  };
  process.on('SIGINT', onSignal);
  process.on('SIGTERM', onSignal);

  const ink = render(
    React.createElement(App, {
      workspace: args.workspace,
      autoApprove: args.autoApprove,
      defaultModel: args.model,
    }),
    {
      exitOnCtrlC: false,
      patchConsole: false,
    },
  );

  ink.waitUntilExit().then(
    () => {
      process.exit(0);
    },
    (err) => {
      console.error('[agenttunnel] fatal:', err);
      process.exit(1);
    },
  );
}

main();
