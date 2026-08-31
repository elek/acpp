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

// Scenario the user hits on a fresh project: opening a project with nothing
// open lands on a freshly started pending session, so the Stop button is live
// from the first paint and the first prompt runs on that session rather than
// swapping in another one.
test('the Stop button is live on the pending session a fresh project opens on', async ({
  page,
  tempProject,
}) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();
  await project.createProject(tempProject.name, tempProject.dir);

  // Opening the project started a pending session and pinned it in the URL.
  await expect
    .poll(() => project.currentSessionId(), { timeout: 30_000 })
    .not.toBeNull();
  const sessionId = project.currentSessionId();
  await expect(project.stopButton).toBeVisible();

  await project.send('Do some slow work.');

  // The streamed chunk confirms a turn is genuinely underway on the server.
  await expect(project.assistantMessages.last()).toContainText('thinking...', {
    timeout: 30_000,
  });

  // The turn runs on the session the project opened on — no second session was
  // created behind it — and the button still targets it.
  await expect(project.stopButton).toBeVisible();
  expect(project.currentSessionId()).toBe(sessionId);
  await expect(project.stopForm).toHaveAttribute(
    'action',
    `/session/${sessionId}/stop`,
  );

  // Clicking it really ends this session. It was the project's only open one, so
  // the window closes with it (see stop-closes-window.stub.spec.ts).
  await project.stop();
  await project.expectWindowClosed(tempProject.name);
});

// The other in-place start: the session-bar "new" button. After a session has
// been stopped the Stop button is hidden and still carries the old session's
// action, so starting a fresh one must both re-show it and re-target it.
//
// Two sessions are opened first: stopping the project's last open session closes
// the window outright, so the in-place swap this exercises only happens while
// another session of the project is still live.
test('the Stop button returns when the "new" button starts another session', async ({
  page,
  tempProject,
}) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();
  await project.createProject(tempProject.name, tempProject.dir);

  // The stub agent never finishes this turn, so this session stays open and
  // holds the window while the others come and go.
  await project.send('Do some slow work.');
  await expect(project.stopButton).toBeVisible();
  const firstSessionId = project.currentSessionId();

  const secondSessionId = await project.startNewConversation(firstSessionId);
  expect(secondSessionId).not.toBe(firstSessionId);

  await project.stop();
  await project.expectStoppedState();
  await expect(project.stopButton).toBeHidden();

  // A fresh session from the "new" button brings the Stop button back, pointed
  // at the new session.
  const thirdSessionId = await project.startNewConversation(secondSessionId);
  expect(thirdSessionId).not.toBe(secondSessionId);
  await expect(project.stopButton).toBeVisible();
  await expect(project.stopForm).toHaveAttribute(
    'action',
    `/session/${thirdSessionId}/stop`,
  );
});
