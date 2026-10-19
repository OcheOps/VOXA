#!/usr/bin/env node
/**
 * Runs Next.js in the web runtime (Voxa Web) without affecting desktop builds.
 *
 *   node scripts/next-web.js dev     -> next dev  (port 3118)
 *   node scripts/next-web.js build   -> next build (static export in out/)
 *
 * Sets NEXT_PUBLIC_VOXA_RUNTIME=web and loads public web settings from
 * .env.web and .env.web.local (the latter wins; real environment variables
 * win over both). Only NEXT_PUBLIC_VOXA_* keys are accepted so server
 * secrets can never be inlined into the browser bundle by mistake.
 */
const { spawnSync } = require('child_process');
const fs = require('fs');
const path = require('path');

const command = process.argv[2];
if (!['dev', 'build'].includes(command)) {
  console.error('Usage: node scripts/next-web.js [dev|build]');
  process.exit(1);
}

const root = path.join(__dirname, '..');
const fileValues = {};
for (const file of ['.env.web', '.env.web.local']) {
  const full = path.join(root, file);
  if (!fs.existsSync(full)) continue;
  for (const line of fs.readFileSync(full, 'utf8').split(/\r?\n/)) {
    const match = line.match(/^\s*([A-Z0-9_]+)\s*=\s*(.*?)\s*$/);
    if (!match || line.trim().startsWith('#')) continue;
    const [, key, raw] = match;
    if (!key.startsWith('NEXT_PUBLIC_VOXA_')) {
      console.error(`${file}: ${key} is not allowed (only public NEXT_PUBLIC_VOXA_* settings belong in the web build)`);
      process.exit(1);
    }
    fileValues[key] = raw.replace(/^(['"])(.*)\1$/, '$2');
  }
}

const env = { ...fileValues, ...process.env, NEXT_PUBLIC_VOXA_RUNTIME: 'web' };
const args = command === 'dev' ? ['dev', '-p', '3118'] : ['build'];
const next = path.join(root, 'node_modules', '.bin', process.platform === 'win32' ? 'next.cmd' : 'next');
const result = spawnSync(next, args, { cwd: root, env, stdio: 'inherit', shell: process.platform === 'win32' });
process.exit(result.status ?? 1);
