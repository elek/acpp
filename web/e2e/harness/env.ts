// Shared constants and small helpers for the web e2e harness. Imported by
// global-setup, global-teardown, the Playwright config and the fixtures so ports,
// paths and the agent matrix are defined in exactly one place.
import { execFileSync } from 'node:child_process';
import * as path from 'node:path';

export const E2E_DIR = path.resolve(__dirname, '..');
export const REPO_ROOT = path.resolve(E2E_DIR, '..', '..');
export const RUN_DIR = path.join(E2E_DIR, '.run');
export const ACPP_BIN = path.join(RUN_DIR, 'bin', 'acpp');
export const STATE_FILE = path.join(RUN_DIR, 'state.json');

// Off-default postgres host port (see docker-compose.yml).
export const DB_PORT = 5434;
export const DB_DSN = `postgres://acpp:acpp@127.0.0.1:${DB_PORT}/acpp?sslmode=disable`;

// One agent === one Playwright project === one acpp server on its own port.
// `command` is the ACP agent the server launches; override via env for local
// smoke runs (e.g. ACPP_E2E_RAI_AGENT="rai acp fake"). `bin` is the executable
// that must exist on PATH for the agent to be considered available; when it is
// missing the server is not started and that project's tests skip.
export interface AgentSpec {
  name: string;
  bin: string;
  command: string;
  port: number;
}

export const AGENTS: AgentSpec[] = [
  {
    name: 'rai',
    bin: 'rai',
    command: process.env.ACPP_E2E_RAI_AGENT || 'rai acp',
    port: 6071,
  },
  {
    name: 'claude-code-acp',
    bin: 'claude-code-acp',
    command: process.env.ACPP_E2E_CLAUDE_AGENT || 'claude-code-acp',
    port: 6072,
  },
];

export function baseURLForAgent(name: string): string {
  const spec = AGENTS.find((a) => a.name === name);
  if (!spec) throw new Error(`unknown agent project: ${name}`);
  return `http://127.0.0.1:${spec.port}`;
}

// onPath reports whether an executable is resolvable on PATH.
export function onPath(bin: string): boolean {
  try {
    execFileSync('sh', ['-c', `command -v ${JSON.stringify(bin)}`], {
      stdio: 'ignore',
    });
    return true;
  } catch {
    return false;
  }
}

// State written by global-setup and consumed by fixtures + global-teardown.
export interface HarnessState {
  // agent name -> its server base URL, for the agents that were actually started.
  servers: Record<string, string>;
  // child process ids to kill on teardown.
  pids: number[];
  // agent name -> its server's process id, so a test can restart a specific
  // server (see harness/restart.ts) and teardown can target the current one.
  serverPids: Record<string, number>;
}
