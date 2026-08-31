import { test, expect } from '../fixtures';
import { ProjectPage } from '../pages/ProjectPage';

// Stopping a session leaves the project window with nothing to do once no other
// session of that project is open, so the whole window closes and the browser
// lands back on the bare desktop.
//
// The old behaviour was a trap: the window stayed on the dead conversation, and
// because opening a project with nothing open starts a fresh pending session,
// any way back into the project produced what looked like the same window again
// — with a new pending session in it — as if the stop had not happened.
//
// These are `.stub.spec.ts`: they run under the deterministic `hang` agent, which
// starts a turn and never responds. A real agent would finish the turn and
// legitimately end the session on its own, racing the assertions.

test('stopping the last open session closes the project window', async ({
  page,
  tempProject,
}) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();
  await project.createProject(tempProject.name, tempProject.dir);

  // Opening the project landed on a pending session; put a turn on it so there
  // is something real to stop.
  await expect
    .poll(() => project.currentSessionId(), { timeout: 30_000 })
    .not.toBeNull();
  await project.send('Do some slow work.');
  await expect(project.assistantMessages.last()).toContainText('thinking...', {
    timeout: 30_000,
  });
  await expect(project.stopButton).toBeVisible();

  await project.stop();

  // The discriminating assertion: the window is gone, not replaced by another
  // one carrying a fresh pending session.
  await project.expectWindowClosed(tempProject.name);

  // And nothing was started behind the scenes: the project is only reachable
  // again by explicitly opening it.
  await expect(project.stopButton).toHaveCount(0);
});

test('stopping one of two open sessions keeps the window', async ({
  page,
  tempProject,
}) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();
  await project.createProject(tempProject.name, tempProject.dir);

  // First session, held in-progress by the stub agent.
  await project.send('Do some slow work.');
  await expect(project.stopButton).toBeVisible();
  const firstSessionId = project.currentSessionId();

  // A second one alongside it. The stub agent never finishes the first, so the
  // project has two open sessions.
  const secondSessionId = await project.startNewConversation(firstSessionId);

  await project.stop();

  // The first session is still open, so the window stays on screen — only this
  // session's prompt bar goes away.
  await project.expectStoppedState();
  expect(project.currentSessionId()).toBe(secondSessionId);
  await expect(project.projectTitle).toHaveText(tempProject.name);
});
