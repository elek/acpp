import { test, expect } from '../fixtures';
import { SessionsPage } from '../pages/SessionsPage';
import { ProjectPage } from '../pages/ProjectPage';

// Scenario 1: create a project, prompt it, stop the session, then start a fresh
// session — verifying the streamed answer, the post-stop prompt-bar state, and
// that a new session accepts a prompt.
test('prompt, stop, and start a new session', async ({ page, tempProject }) => {
  // 1. New project: tempProject already made a temp dir + git repo + README.
  //    Bootstrap it into a session via the New Session modal.
  const sessions = new SessionsPage(page);
  await sessions.goto();
  await sessions.createSession(tempProject.dir);

  // 2. Add a prompt on the project view.
  const project = new ProjectPage(page);
  await project.goto(tempProject.name);
  await project.send('What is the capital of Hungary');

  // 3. The streamed answer should reference Hungary or its capital (tolerant,
  //    since real agents are non-deterministic).
  await project.waitForResponse();
  expect(await project.responseText()).toMatch(/hungary|budapest/i);

  // 4. Stop the running session.
  await project.stop();

  // 5. Send/Cancel gone, text area remains, a "New session" button appears.
  await project.expectStoppedState();

  // 6. Start a new session and ask the recall question.
  await project.startNewSession();
  await project.send('what was the previous question');

  // 7. Loose assert: a session started and some answer came back.
  await project.waitForResponse();
  expect((await project.responseText()).length).toBeGreaterThan(0);
});
