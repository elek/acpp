// Global setup: bring up an empty postgres, build acpp, and start one acpp web
// server per available agent (each on its own port with a private config). The
// resulting server URLs and child pids are written to state.json for the
// fixtures and for global-teardown. Runs once before the whole suite.
import { execFileSync, spawn } from 'node:child_process';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import {
  ACPP_BIN,
  AGENTS,
  baseURLForAgent,
  DB_PORT,
  E2E_DIR,
  HarnessState,
  onPath,
  REPO_ROOT,
  RUN_DIR,
  STATE_FILE,
} from './env';
import { configPath, renderConfig } from './config-template';

const COMPOSE = ['compose', '-f', path.join(E2E_DIR, 'docker-compose.yml')];

function sh(cmd: string, args: string[], cwd?: string): void {
  execFileSync(cmd, args, { cwd, stdio: 'inherit' });
}

async function waitHealthy(url: string, timeoutMs = 60_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${url}/api/health`);
      if (res.ok) return;
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`server at ${url} did not become healthy in ${timeoutMs}ms`);
}

function binDir(bin: string): string {
  const out = execFileSync('sh', ['-c', `command -v ${JSON.stringify(bin)}`])
    .toString()
    .trim();
  return path.dirname(out);
}

async function globalSetup(): Promise<void> {
  fs.rmSync(RUN_DIR, { recursive: true, force: true });
  fs.mkdirSync(path.dirname(ACPP_BIN), { recursive: true });

  // Fresh, empty postgres. `down -v` drops any leftover volume from a prior run
  // so the database starts empty; `up --wait` blocks until the healthcheck passes.
  console.log(`>> postgres (docker, host port ${DB_PORT})…`);
  try {
    sh('docker', [...COMPOSE, 'down', '-v']);
  } catch {
    /* nothing to tear down */
  }
  sh('docker', [...COMPOSE, 'up', '-d', '--wait']);

  console.log(`>> building acpp -> ${ACPP_BIN}`);
  sh('go', ['build', '-o', ACPP_BIN, '.'], REPO_ROOT);

  const searchDir = fs.mkdtempSync(path.join(os.tmpdir(), 'acpp-e2e-projects-'));
  const state: HarnessState = { servers: {}, pids: [], serverPids: {} };

  for (const agent of AGENTS) {
    if (!onPath(agent.bin)) {
      console.log(`>> agent '${agent.name}' (${agent.bin}) not on PATH — skipping`);
      continue;
    }

    const configHome = path.join(RUN_DIR, agent.name);
    fs.mkdirSync(path.join(configHome, 'acpp'), { recursive: true });
    fs.writeFileSync(
      configPath(configHome),
      renderConfig(agent, binDir(agent.bin), searchDir),
    );

    const url = baseURLForAgent(agent.name);
    const logPath = path.join(configHome, 'server.log');
    const logFd = fs.openSync(logPath, 'w');
    console.log(`>> starting acpp web for '${agent.name}' on ${url} (${agent.command})`);
    // detached makes the child a process-group leader so a restart test can kill
    // the whole tree (server + agent) with process.kill(-pid, …).
    const child = spawn(ACPP_BIN, ['web', '--addr', `:${agent.port}`], {
      env: { ...process.env, XDG_CONFIG_HOME: configHome },
      stdio: ['ignore', logFd, logFd],
      detached: true,
    });
    child.unref();
    if (child.pid) {
      state.pids.push(child.pid);
      state.serverPids[agent.name] = child.pid;
    }

    try {
      await waitHealthy(url);
    } catch (err) {
      console.error(`--- ${agent.name} server.log ---`);
      console.error(fs.readFileSync(logPath, 'utf8').split('\n').slice(-40).join('\n'));
      throw err;
    }
    state.servers[agent.name] = url;
    console.log(`>> '${agent.name}' healthy`);
  }

  if (Object.keys(state.servers).length === 0) {
    console.warn(
      '>> no agents available (rai / claude-code-acp not on PATH) — all tests will skip',
    );
  }

  fs.writeFileSync(STATE_FILE, JSON.stringify(state, null, 2));
}

export default globalSetup;
