import { test, expect } from '../fixtures';
import { SessionsPage } from '../pages/SessionsPage';
import { ProjectPage } from '../pages/ProjectPage';

// Scenario: a harness command typed in the web UI (/help) is echoed as a
// distinct command block and its response is shown in the session window — and
// because command I/O is transient (never persisted), neither survives a reload.
test('slash command is echoed with its response and is not persisted', async ({ page, tempProject }) => {
  const sessions = new SessionsPage(page);
  await sessions.goto();
  await sessions.createSession(tempProject.dir);

  const project = new ProjectPage(page);
  await project.goto(tempProject.name);

  // /help is handled entirely by the harness (no agent round-trip), so its
  // output is deterministic — ideal for asserting the command display.
  await project.send('/help');

  // The command is echoed as "> /help" in the distinct command block.
  await expect(project.commandEchoes.last()).toHaveText('> /help', { timeout: 30_000 });

  // The response lists the built-in harness commands.
  await expect(project.commandResponses.last()).toContainText('Harness commands:', { timeout: 30_000 });
  await expect(project.commandResponses.last()).toContainText('/clear');

  // Transient: a reload replays only persisted history, so the command and its
  // response must be gone.
  await page.reload();
  await expect(project.promptInput).toBeVisible();
  await expect(project.commandEchoes).toHaveCount(0);
  await expect(project.commandResponses).toHaveCount(0);
});
