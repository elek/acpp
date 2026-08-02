import { test, expect } from '../fixtures';
import { ProjectPage } from '../pages/ProjectPage';

// The session bar's Stop button must appear the moment a session starts, without
// a reload. Both ways of starting one from the project view swap the session in
// place (history.pushState, no navigation), so the Stop button — which the
// server renders only for a running/pending session — has to be driven by the
// client as part of that swap.
//
// These are `.stub.spec.ts`: they run under the deterministic `hang` agent,
// which starts a turn and never responds. A real agent would finish the turn
// almost immediately and legitimately hide the Stop button again, racing every
// assertion here.

// Scenario the user hits on a fresh project: no session exists, so the server
// renders no Stop button at all. Submitting the first prompt creates a session
// in-place and the button must show up right away — before the fix it appeared
// only after a manual reload.
test('the Stop button appears when the first prompt starts a new session', async ({
  page,
  tempProject,
}) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();
  await project.createProject(tempProject.name, tempProject.dir);

  // No session yet: there is nothing to stop.
  await expect(project.stopButton).toBeHidden();

  await project.send('Do some slow work.');

  // The streamed chunk confirms a turn is genuinely underway on the server.
  await expect(project.assistantMessages.last()).toContainText('thinking...', {
    timeout: 30_000,
  });

  // The discriminating assertion: visible with no reload in between.
  await expect(project.stopButton).toBeVisible();

  // And it targets the session that was just created, not a stale one.
  const sessionId = project.currentSessionId();
  expect(sessionId).toBeTruthy();
  await expect(project.stopForm).toHaveAttribute(
    'action',
    `/session/${sessionId}/stop`,
  );

  // Clicking it really ends this session: the prompt bar goes away and the
  // button with it.
  await project.stop();
  await project.expectStoppedState();
  await expect(project.stopButton).toBeHidden();
});

// The other in-place start: the session-bar "new" button. After a session has
// been stopped the Stop button is hidden and still carries the old session's
// action, so starting a fresh one must both re-show it and re-target it.
test('the Stop button returns when the "new" button starts another session', async ({
  page,
  tempProject,
}) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();
  await project.createProject(tempProject.name, tempProject.dir);

  await project.send('Do some slow work.');
  await expect(project.stopButton).toBeVisible();
  const firstSessionId = project.currentSessionId();

  await project.stop();
  await project.expectStoppedState();
  await expect(project.stopButton).toBeHidden();

  // A fresh session from the "new" button brings the Stop button back, pointed
  // at the new session.
  const secondSessionId = await project.startNewConversation(firstSessionId);
  expect(secondSessionId).not.toBe(firstSessionId);
  await expect(project.stopButton).toBeVisible();
  await expect(project.stopForm).toHaveAttribute(
    'action',
    `/session/${secondSessionId}/stop`,
  );
});
