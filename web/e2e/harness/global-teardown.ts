// Global teardown: stop the acpp servers started by global-setup and drop the
// dockerized postgres (and its volume, so the next run starts empty).
import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { E2E_DIR, HarnessState, STATE_FILE } from './env';

const COMPOSE = ['compose', '-f', path.join(E2E_DIR, 'docker-compose.yml')];

async function globalTeardown(): Promise<void> {
  if (fs.existsSync(STATE_FILE)) {
    const state: HarnessState = JSON.parse(fs.readFileSync(STATE_FILE, 'utf8'));
    for (const pid of state.pids) {
      try {
        process.kill(pid, 'SIGTERM');
      } catch {
        /* already gone */
      }
    }
  }

  try {
    execFileSync('docker', [...COMPOSE, 'down', '-v'], { stdio: 'inherit' });
  } catch {
    /* best effort */
  }
}

export default globalTeardown;
