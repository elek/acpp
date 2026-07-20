import { test, expect } from '../fixtures';
import { SessionsPage } from '../pages/SessionsPage';
import { ProjectPage } from '../pages/ProjectPage';

// The project view always offers a "new" conversation button in the top session
// bar. Clicking it starts a fresh (promptless) session, navigates to it, and
// rewrites the URL so a browser reload lands on the same session. After reload
// the server renders the real persisted session — status "pending" and a
// date-stamped selector label — rather than the client-side "New session"
// placeholder. Submitting a prompt then drives the session to "running", which
// a subsequent reload reflects.
test('new button starts a session that survives reload and runs on prompt', async ({
  page,
  tempProject,
}) => {
  // Bootstrap: create a first session (and thus the project) via the modal.
  const sessions = new SessionsPage(page);
  await sessions.goto();
  const firstSessionId = await sessions.createSession(tempProject.dir);

  // Open the project view for that project.
  const project = new ProjectPage(page);
  await project.goto(tempProject.name);

  // The "new" button is always present in the session bar.
  await expect(project.newConversationButton).toBeVisible();

  // Clicking it creates a fresh session and navigates to it (URL rewritten to a
  // new `session` id).
  const newSessionId = await project.startNewConversation(firstSessionId);
  expect(newSessionId).not.toBe(firstSessionId);

  // Reloading lands on the same session — the URL carries it server-side — and
  // the promptless session is persisted as "pending".
  await project.reloadAndExpectStatus('pending');
  expect(project.currentSessionId()).toBe(newSessionId);

  // The dropdown shows a real date stamp, not the pre-reload "New session"
  // placeholder text.
  const label = await project.selectedSessionLabel();
  expect(label).not.toContain('New session');
  expect(label).toMatch(/^[A-Z][a-z]{2}\s+\d/); // e.g. "Jul 20 15:04 — pending"

  // Submitting a prompt runs a turn; once it completes the status is flushed to
  // "running", which a reload reflects.
  await project.send('Say hello in one short word.');
  await project.waitForResponse();
  await project.reloadAndExpectStatus('running');
  expect(project.currentSessionId()).toBe(newSessionId);
});
