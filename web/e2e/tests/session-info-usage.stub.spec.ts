import { test, expect } from '../fixtures';
import { SessionsPage } from '../pages/SessionsPage';
import { ProjectPage } from '../pages/ProjectPage';

// The Session Info side panel's Cost and Context Window figures must update while
// a turn is in progress, not only after it finishes. The agent reports these via
// a `usage_update` notification that reaches the browser over the per-session
// WebSocket mid-turn — before the DB row is flushed at the turn boundary — so the
// panel is patched live in the client from the streamed event.
//
// This is inherently mid-turn behaviour, so it runs under the deterministic
// `hang` agent (a `.stub.spec.ts`): the stub streams a chunk and a usage_update
// (size 200000, used 50000, cost $0.42), then never responds, holding the turn
// in progress. A real/fake agent finishes far too fast to reliably observe this.
test('the session info panel updates cost and context live during a turn', async ({
  page,
  tempProject,
}) => {
  const sessions = new SessionsPage(page);
  await sessions.goto();
  const sessionId = await sessions.createSession(tempProject.dir);

  const project = new ProjectPage(page);
  await page.goto(
    `/projects?project=${encodeURIComponent(tempProject.name)}&session=${encodeURIComponent(sessionId)}`,
  );
  await expect(project.promptInput).toBeVisible();

  // Submit a prompt. The hang agent streams a chunk then a usage_update, then
  // never responds, so the turn — and the reported usage — stay live.
  await project.send('Do some slow work.');

  // The streamed chunk confirms the turn is genuinely underway on the server.
  await expect(project.assistantMessages.last()).toContainText('thinking...', {
    timeout: 30_000,
  });

  // Open the panel and assert it reflects the mid-turn usage_update. These values
  // are patched into the DOM by the live WebSocket event, not read from the DB
  // (which is not flushed until the turn ends). The assertions poll, tolerating
  // the event arriving just after the chunk.
  await project.openInfoPanel();
  await expect(project.infoContextCount).toHaveText('50K / 200K', { timeout: 30_000 });
  await expect(project.infoContextPct).toHaveText('25% used');
  await expect(project.infoCost).toHaveText('$0.4200');
});
