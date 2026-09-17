import { test, expect } from '../fixtures';
import { ProjectPage } from '../pages/ProjectPage';

// An unsent prompt survives leaving the page. The bottom taskbar tabs are plain
// links, so switching project or session is a full page load and anything typed
// but not sent went away with the document — the half-written prompt you left to
// go look something up in another tab was simply gone when you came back.
//
// Drafts are keyed per session, so two sessions holding two different unfinished
// thoughts do not overwrite each other, and a draft is deleted the moment it is
// actually submitted.
//
// These are `.stub.spec.ts`: they run under the deterministic `hang` agent,
// which starts a turn and never responds. A real agent would finish the turn and
// end the session, hiding the prompt bar these tests type into.

test('an unsent prompt is restored after navigating away and back', async ({
  page,
  tempProject,
}) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();
  await project.createProject(tempProject.name, tempProject.dir);

  await expect
    .poll(() => project.currentSessionId(), { timeout: 30_000 })
    .not.toBeNull();
  const sessionId = project.currentSessionId()!;

  const draft = 'half-written thought I have not sent yet';
  await project.typeDraft(draft, sessionId);

  // Leave the window entirely — the same full page load a taskbar tab performs.
  await project.gotoProjects();
  await expect(project.promptInput).toHaveCount(0);

  await project.gotoSession(tempProject.name, sessionId);

  // The discriminating assertion: the box comes back populated, not empty.
  await expect(project.promptInput).toHaveValue(draft);
});

test('drafts are kept per session, not shared', async ({ page, tempProject }) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();
  await project.createProject(tempProject.name, tempProject.dir);

  // The stub agent never finishes this turn, so the first session stays open
  // and the project ends up with two live sessions to switch between.
  await project.send('Do some slow work.');
  await expect(project.stopButton).toBeVisible();
  const first = project.currentSessionId()!;

  const second = await project.startNewConversation(first);

  // A distinct unfinished prompt in each session.
  await project.gotoSession(tempProject.name, second);
  await project.typeDraft('draft for the second session', second);
  await project.gotoSession(tempProject.name, first);
  await project.typeDraft('draft for the first session', first);

  // Neither clobbered the other: each session shows its own.
  await expect(project.promptInput).toHaveValue('draft for the first session');
  await project.gotoSession(tempProject.name, second);
  await expect(project.promptInput).toHaveValue('draft for the second session');
});

test('a draft typed before the window had a session is adopted by it', async ({
  page,
  tempProject,
}) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();
  await project.createProject(tempProject.name, tempProject.dir);

  await expect
    .poll(() => project.currentSessionId(), { timeout: 30_000 })
    .not.toBeNull();
  const sessionId = project.currentSessionId()!;

  // A project window with no session yet has no session id to key on, so its
  // draft is stored against the project. That state is transient — re-opening a
  // project auto-opens a pending session — so seed the entry directly rather
  // than trying to catch the window before its session exists.
  const draft = 'typed before any session existed';
  await project.seedDraft(`project:${tempProject.name}`, draft);

  await project.gotoSession(tempProject.name, sessionId);

  // Looking only under the session key would have missed this entirely.
  await expect(project.promptInput).toHaveValue(draft);
  // Adopted, not copied: it now belongs to this session and cannot resurface
  // in some later one.
  await expect.poll(() => project.storedDraft(sessionId)).toBe(draft);
  await expect
    .poll(() => project.storedDraft(`project:${tempProject.name}`))
    .toBe('');
});

test('sending a prompt deletes its draft', async ({ page, tempProject }) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();
  await project.createProject(tempProject.name, tempProject.dir);

  await expect
    .poll(() => project.currentSessionId(), { timeout: 30_000 })
    .not.toBeNull();
  const sessionId = project.currentSessionId()!;

  await project.typeDraft('this one actually gets sent', sessionId);
  await project.send('this one actually gets sent');

  // Submitted, so it is no longer a draft: gone from storage, and the box is
  // still empty after a reload rather than re-offering what was already sent.
  await expect.poll(() => project.storedDraft(sessionId)).toBe('');
  await project.gotoSession(tempProject.name, sessionId);
  await expect(project.promptInput).toHaveValue('');
});
