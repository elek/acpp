import { test, expect } from '../fixtures';
import { SessionsPage } from '../pages/SessionsPage';
import { ProjectPage } from '../pages/ProjectPage';
import { ProjectDetailPage } from '../pages/ProjectDetailPage';

// Clicking a project's name in the session view opens its overview page
// (/project/<name>): a heading, a configuration table, and the project's
// sessions rendered as cards. The bootstrapped, promptless session is pending,
// so it lands in the Active grid and its "Open →" link returns to that session.
// This asserts UI/flow only (no agent output), so it holds under any agent.
test('project name opens the overview page with a session card', async ({
  page,
  tempProject,
}) => {
  // Bootstrap: one promptless (pending) session, which also creates the project.
  const sessions = new SessionsPage(page);
  await sessions.goto();
  const sessionId = await sessions.createSession(tempProject.dir);

  // Open the project's session view and click its name to open the overview.
  const project = new ProjectPage(page);
  await project.goto(tempProject.name);
  await expect(project.projectTitle).toHaveText(tempProject.name);
  await project.projectTitle.click();

  // Landed on /project/<name> with the project as its heading.
  const detail = new ProjectDetailPage(page);
  await page.waitForURL(
    (url) => url.pathname === `/project/${tempProject.name}`,
    { timeout: 30_000 },
  );
  await expect(detail.heading).toHaveText(tempProject.name);

  // The renamed top nav marks "Workplace" as the active tab.
  await expect(detail.navWorkplace).toHaveClass(/active/);

  // The configuration table surfaces the project's stored directory.
  await expect(detail.configTable).toContainText(tempProject.dir);

  // The bootstrapped pending session shows as a single Active card whose
  // "Open →" link points back to it; there are no closed sessions yet.
  await expect(detail.activeCards).toHaveCount(1);
  await expect(detail.closedCards).toHaveCount(0);
  await expect(detail.cardOpenLinks.first()).toHaveAttribute(
    'href',
    new RegExp(`session=${sessionId}`),
  );

  // Opening the card returns to the session view for that exact session.
  await detail.openFirstCard();
  const url = new URL(page.url());
  expect(url.searchParams.get('project')).toBe(tempProject.name);
  expect(url.searchParams.get('session')).toBe(sessionId);
});
