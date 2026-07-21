// restartServer stops and restarts a single agent's acpp serve server, the way a
// real deploy/crash-recovery would. Tests use it to prove that sessions left
// active by the previous process are completed once a new process comes up.
//
// The signal decides how the old process dies:
//   SIGKILL  — kill the whole process group (server + agent), simulating a hard
//              crash / kill -9 that finalizes nothing.
//   SIGTERM  — signal the leader only, letting the server shut down gracefully
//              (it finalizes its conversations on the way out).
import { spawn } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';
import {
  ACPP_BIN,
  AGENTS,
  HarnessState,
  RUN_DIR,
  STATE_FILE,
  baseURLForAgent,
} from './env';

async function isHealthy(url: string): Promise<boolean> {
  try {
    const res = await fetch(`${url}/api/health`);
    return res.ok;
  } catch {
    return false;
  }
}

async function waitFor(
  predicate: () => Promise<boolean>,
  what: string,
  timeoutMs = 60_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await new Promise((r) => setTimeout(r, 300));
  }
  throw new Error(`timed out waiting for ${what} after ${timeoutMs}ms`);
}

export async function restartServer(
  name: string,
  signal: 'SIGKILL' | 'SIGTERM' = 'SIGKILL',
): Promise<void> {
  const spec = AGENTS.find((a) => a.name === name);
  if (!spec) throw new Error(`unknown agent project: ${name}`);

  const state: HarnessState = JSON.parse(fs.readFileSync(STATE_FILE, 'utf8'));
  const url = baseURLForAgent(name);
  const configHome = path.join(RUN_DIR, name);
  const oldPid = state.serverPids?.[name];

  if (oldPid) {
    try {
      // SIGKILL takes the whole group so no agent lingers; SIGTERM goes to the
      // leader alone so it can stop its agent cleanly and finalize sessions.
      if (signal === 'SIGKILL') process.kill(-oldPid, 'SIGKILL');
      else process.kill(oldPid, 'SIGTERM');
    } catch {
      /* already gone */
    }
  }

  // Wait until the old server stops answering before rebinding the same port.
  await waitFor(async () => !(await isHealthy(url)), `${name} server to stop`, 30_000);

  const logFd = fs.openSync(path.join(configHome, 'server.log'), 'a');
  const child = spawn(ACPP_BIN, ['serve', '--addr', `:${spec.port}`], {
    env: { ...process.env, XDG_CONFIG_HOME: configHome },
    stdio: ['ignore', logFd, logFd],
    detached: true,
  });
  child.unref();

  // Record the new pid so a later restart and teardown target the live process.
  state.serverPids = { ...(state.serverPids ?? {}), [name]: child.pid! };
  state.pids = Object.values(state.serverPids);
  fs.writeFileSync(STATE_FILE, JSON.stringify(state, null, 2));

  await waitFor(() => isHealthy(url), `${name} server to become healthy`);
}
