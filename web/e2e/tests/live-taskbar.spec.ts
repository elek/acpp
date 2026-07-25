import { test, expect } from '../fixtures';
import { SessionsPage } from '../pages/SessionsPage';
import { ProjectPage } from '../pages/ProjectPage';

// The bottom taskbar subscribes to a global lifecycle WebSocket (/events/ws) and
// re-fetches its fragment whenever a session is created, finished, or replaced
// anywhere — so it reflects new work without a full page reload. Here we open a
// project's view, then create a second session in that project out-of-band (a
// direct API POST, no navigation) and assert a new status dot appears live.
test('taskbar adds a dot live when a new session is created', async ({
  page,
  tempProject,
}) => {
  // Bootstrap: one promptless (pending) session, which also creates the project.
  const sessions = new SessionsPage(page);
  await sessions.goto();
  await sessions.createSession(tempProject.dir);

  // Open the project view; its taskbar now carries one dot for that session and
  // the page has opened its lifecycle socket.
  const project = new ProjectPage(page);
  await project.goto(tempProject.name);

  // Scope to THIS project's (active) tab: the shared DB means other projects'
  // active sessions also render dots, so a global count would be non-isolated.
  const dots = page.locator('.taskbar-tab.active .taskbar-dot');
  await expect(dots).toHaveCount(1);

  // Create another session in the same project without touching this page. The
  // server uses its default agent, so no agent id is needed here.
  const resp = await page.request.post('/projects/session', {
    data: { project: tempProject.name, dir: tempProject.dir },
  });
  expect(resp.ok()).toBeTruthy();

  // The lifecycle nudge drives a fragment re-fetch: a second dot appears with no
  // reload.
  await expect(dots).toHaveCount(2, { timeout: 15_000 });
});
