import { build } from 'esbuild';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const __dirname = dirname(fileURLToPath(import.meta.url));
const pkg = JSON.parse(readFileSync(resolve(__dirname, 'package.json'), 'utf8'));

const outfile = resolve(__dirname, 'dist/cli.js');

await build({
  entryPoints: [resolve(__dirname, 'src/cli.tsx')],
  bundle: true,
  platform: 'node',
  target: 'node20',
  format: 'esm',
  outfile,
  external: [
    'ink',
    'react',
    'react-dom',
    'zustand',
    ...Object.keys(pkg.dependencies || {}),
  ],
  jsx: 'automatic',
  loader: {
    '.tsx': 'tsx',
    '.ts': 'ts',
  },
  minify: false,
  sourcemap: true,
  logLevel: 'info',
});

let contents = readFileSync(outfile, 'utf8');
if (!contents.startsWith('#!')) {
  contents = '#!/usr/bin/env node\n' + contents;
} else {
  // esbuild sometimes prepends its own shebang even when we didn't ask for one.
  // Collapse N leading shebang lines to a single one.
  const lines = contents.split('\n');
  const shebangs = [];
  while (lines.length > 0 && /^#!/.test(lines[0])) {
    shebangs.push(lines.shift());
  }
  contents = `${shebangs[0] || '#!/usr/bin/env node'}\n${lines.join('\n')}`;
}
writeFileSync(outfile, contents);

