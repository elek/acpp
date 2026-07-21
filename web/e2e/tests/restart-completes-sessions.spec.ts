import { test, expect } from '../fixtures';
import { SessionsPage } from '../pages/SessionsPage';
import { restartServer } from '../harness/restart';

// A session left active by one process must be marked complete once a new
// process comes up (cli/web.go completes stale running/pending sessions on
// startup). This is verified for both ways the previous process can die:
//
//   kill -9 (SIGKILL) — the process finalizes nothing; the session is left
//     stuck 'running' and only the restart's startup cleanup can complete it.
//   graceful (SIGTERM) — the process finalizes its conversation on the way out,
//     so the session is already complete before the restart; it stays complete.
//
// Either way, after the restart no session is left active.

// The API maps running/pending -> 'running' and complete -> 'done'.
async function sessionStatus(baseURL: string, id: string): Promise<string> {
  try {
    const res = await fetch(`${baseURL}/api/session/${encodeURIComponent(id)}`);
    if (!res.ok) return `http_${res.status}`;
    const body = (await res.json()) as { status: string };
    return body.status;
  } catch {
    // The server is briefly unreachable while it restarts.
    return 'unreachable';
  }
}

for (const mode of [
  { name: 'kill -9', signal: 'SIGKILL' as const },
  { name: 'graceful SIGTERM', signal: 'SIGTERM' as const },
]) {
  test(`active sessions are completed after a ${mode.name} restart`, async ({
    page,
    tempProject,
    server,
  }) => {
    // Create a session (and its project) from the temp working dir. With no
    // prompt sent it stays active until the process closes it.
    const sessions = new SessionsPage(page);
    await sessions.goto();
    const id = await sessions.createSession(tempProject.dir);

    // It is active before the restart.
    await expect
      .poll(() => sessionStatus(server.baseURL, id), { timeout: 30_000 })
      .toBe('running');

    // Bounce the server the given way.
    await restartServer(server.agent, mode.signal);

    // After the restart the session is finalized ('done'), not left active.
    await expect
      .poll(() => sessionStatus(server.baseURL, id), { timeout: 30_000 })
      .toBe('done');
  });
}
