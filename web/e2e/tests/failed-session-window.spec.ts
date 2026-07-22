import { test, expect } from '../fixtures';
import { ProjectPage } from '../pages/ProjectPage';

// A project with no resolvable working directory must still be usable from the
// web UI. This is the reported bug: the "new" button was hidden for dir-less
// projects, so they were dead ends. Now the button is always offered, and
// starting a session for such a project does not silently fail — when the
// directory cannot be resolved from search_path the conversation is persisted as
// an error, and its window shows a harness error message (rendered from a
// _meta.acpp.type=error agent_message_chunk) explaining the ACP session could not
// be created. This flow spawns no agent, so it is deterministic regardless of the
// project's configured agent.
test('dir-less project offers "new" and shows a persisted failure window', async ({
  page,
  server,
}) => {
  const ghost = `ghost-${Date.now()}`;

  // Arrange: create a dir-less project by attempting a session with no dir. The
  // server cannot resolve the name from search_path, so it records a stillborn
  // conversation and reports failed=true (still a 201 — the window exists).
  const resp = await page.request.post(`${server.baseURL}/projects/session`, {
    data: { project: ghost, dir: '' },
  });
  expect(resp.status()).toBe(201);
  const body = await resp.json();
  expect(body.failed).toBe('true');
  expect(body.id).toBeTruthy();

  // The stillborn window shows the harness error, not agent output.
  const project = new ProjectPage(page);
  await page.goto(`/projects?project=${encodeURIComponent(ghost)}&session=${encodeURIComponent(body.id)}`);
  await expect(project.errorMessages.first()).toBeVisible();
  await expect(project.errorMessages.first()).toContainText('no directory named');

  // The reported bug: the session-bar "new" button is present even though the
  // project has no directory. Clicking it drives the same failure and navigates
  // to a fresh error window (a distinct conversation id).
  await project.goto(ghost);
  await expect(project.newConversationButton).toBeVisible();

  const prev = project.currentSessionId();
  const newId = await project.startNewConversation(prev);
  expect(newId).not.toBe(body.id);
  await expect(project.errorMessages.first()).toBeVisible();
  await expect(project.errorMessages.first()).toContainText(ghost);
});
