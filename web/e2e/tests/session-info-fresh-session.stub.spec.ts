import { test, expect } from '../fixtures';
import { SessionsPage } from '../pages/SessionsPage';
import { ProjectPage } from '../pages/ProjectPage';

// A brand-new session on the web — created but with no prompt submitted yet, so
// the ACP handshake has not populated the model — must show zeroed Cost, Model
// and Context Window in the Session Info panel. Before the fix, the context
// window silently fell back to the per-model default of 200K even though no
// model had been chosen, producing a misleading "0 / 200K" on a session that
// had not actually started.
//
// Runs as a stub spec (always available, no credentials): the assertions only
// need the pre-turn state, which is identical under every agent.
test('a fresh session shows zeroed cost / model / context in the info panel', async ({
  page,
  tempProject,
}) => {
  const sessions = new SessionsPage(page);
  await sessions.goto();
  const sessionId = await sessions.createSession(tempProject.dir);

  // Land on the project view for this session without sending a prompt — the
  // persister has not yet flushed a model or usage_update, so every derived
  // figure in the panel must be zero. This must hold both for the initial
  // server render AND after the authoritative /api refresh triggered by
  // opening the panel — before the fix, the API re-populated the count as
  // "0 / 200K" from the per-model window fallback.
  const project = new ProjectPage(page);
  await page.goto(
    `/projects?project=${encodeURIComponent(tempProject.name)}&session=${encodeURIComponent(sessionId)}`,
  );
  await expect(project.promptInput).toBeVisible();

  await project.openInfoPanel();
  await expect(project.infoCost).toHaveText('$0.0000');
  await expect(project.infoContextCount).toHaveText('0 / 0');
  await expect(project.infoContextPct).toHaveText('0% used');
});
