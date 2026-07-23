import { test, expect } from '../fixtures';
import { SessionsPage } from '../pages/SessionsPage';
import { ProjectPage } from '../pages/ProjectPage';

// A conversation must be persisted as "running" the moment its turn begins —
// not only once the turn's response lands. Before the fix, the persister marked
// the session running only in memory at turn start (beginTurn) and flushed it to
// the store only at turn end (endTurn/PromptResponse); so a conversation
// actively processing was left server-rendered as "pending" for the whole turn.
//
// This is inherently mid-turn behaviour, so it can only be observed while a turn
// is still in progress. A real/fake agent finishes far too fast to reliably
// reload into that window, so this is a `.stub.spec.ts`: it runs only under the
// deterministic `hang` agent project (see harness/env.ts + playwright.config.ts),
// whose stub starts a turn and never sends the response — holding the
// conversation in-progress indefinitely. That project always runs (it launches
// under `node`), so this test is always exercised by `npm run e2e`.
//
// The reload asserts the *server-rendered* status (the live badge is flipped to
// "running" client-side on send regardless of persistence, so only a reload
// reads the DB truth). Before the fix this reload observes "pending" forever and
// the test times out; after the fix it reads "running".
test('a conversation persists as running while its turn is in progress', async ({
  page,
  tempProject,
}) => {
  const sessions = new SessionsPage(page);
  await sessions.goto();
  const sessionId = await sessions.createSession(tempProject.dir);

  // Navigate to the explicit session URL so a reload lands back on the same
  // conversation (a bare ?project= URL only gains its ?session= after a
  // client-side rewrite, which a reload would drop).
  const project = new ProjectPage(page);
  await page.goto(
    `/projects?project=${encodeURIComponent(tempProject.name)}&session=${encodeURIComponent(sessionId)}`,
  );
  await expect(project.promptInput).toBeVisible();

  // Submit a prompt. The hang agent streams one chunk then never responds, so
  // the turn stays in progress for the rest of the test.
  await project.send('Do some slow work.');

  // The streamed chunk confirms the turn is genuinely underway on the server.
  await expect(project.assistantMessages.last()).toContainText('thinking...', {
    timeout: 30_000,
  });

  // The discriminating assertion: a reload renders the *persisted* status, which
  // must be "running" mid-turn — not the pre-turn "pending". (reloadAndExpectStatus
  // retries the reload, so it tolerates the async status flush.)
  await project.reloadAndExpectStatus('running');
  expect(project.currentSessionId()).toBe(sessionId);
});
