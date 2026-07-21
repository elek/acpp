// Custom Playwright fixtures for the web e2e suite.
//
//   tempProject — a throwaway working dir (git repo + one README), auto-cleaned.
//   server      — the running server's base URL + agent id for this project;
//                 tests skip when that agent's server was not started.
import { test as base, expect } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { HarnessState, STATE_FILE, baseURLForAgent } from '../harness/env';

export interface TempProject {
  dir: string;
  name: string;
}

export interface ServerInfo {
  baseURL: string;
  agent: string;
}

function readState(): HarnessState {
  if (!fs.existsSync(STATE_FILE)) return { servers: {}, pids: [], serverPids: {} };
  return JSON.parse(fs.readFileSync(STATE_FILE, 'utf8'));
}

function git(dir: string, ...args: string[]): void {
  execFileSync('git', ['-C', dir, ...args], {
    stdio: 'ignore',
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: 'acpp-e2e',
      GIT_AUTHOR_EMAIL: 'e2e@acpp.test',
      GIT_COMMITTER_NAME: 'acpp-e2e',
      GIT_COMMITTER_EMAIL: 'e2e@acpp.test',
    },
  });
}

export const test = base.extend<{ tempProject: TempProject; server: ServerInfo }>({
  tempProject: async ({}, use) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'acpp-e2e-proj-'));
    git(dir, 'init', '-q');
    fs.writeFileSync(path.join(dir, 'README.md'), '# e2e test project\n');
    git(dir, 'add', 'README.md');
    git(dir, 'commit', '-q', '-m', 'initial commit');
    await use({ dir, name: path.basename(dir) });
    fs.rmSync(dir, { recursive: true, force: true });
  },

  server: [
    async ({}, use) => {
      const name = test.info().project.name;
      const url = readState().servers[name];
      test.skip(!url, `no server started for agent '${name}'`);
      await use({ baseURL: url ?? baseURLForAgent(name), agent: name });
    },
    { auto: true },
  ],
});

export { expect };
