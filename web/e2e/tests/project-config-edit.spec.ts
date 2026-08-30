import { test, expect } from '../fixtures';
import { SessionsPage } from '../pages/SessionsPage';
import { ProjectDetailPage } from '../pages/ProjectDetailPage';

// The Configuration card on /project/<name> is editable: each field has a pencil
// that opens a popup, and saving persists to the project row. This is the browser
// half of the feature — the Go tests cover the endpoint, and the router tests
// cover the stored value reaching a session. Asserts UI/flow only, so it holds
// under any agent.
test('sandbox profiles can be edited from the project overview page', async ({
  page,
  tempProject,
}) => {
  // Bootstrap: one promptless session, which also creates the project row.
  const sessions = new SessionsPage(page);
  await sessions.goto();
  await sessions.createSession(tempProject.dir);

  const detail = new ProjectDetailPage(page);
  await detail.goto(tempProject.name);

  // Unset fields still render a row — that row is the only place to set them.
  await expect(detail.configValue('sandbox_profiles')).toHaveText('(unset)');

  // Tick an enumerated profile. "docker" ships in the embedded sandbox config,
  // so it is always offered.
  await detail.openEditor('sandbox_profiles');
  await detail.profileCheckbox('docker').check();
  await detail.saveEditor();

  // The row shows the new value once the page has come back from the server.
  await expect(detail.configValue('sandbox_profiles')).toHaveText('docker');

  // And the value survives a reload, i.e. it reached the database.
  await page.reload();
  await expect(detail.configValue('sandbox_profiles')).toHaveText('docker');

  // Reopening reflects the stored value as a ticked box rather than free text.
  await detail.openEditor('sandbox_profiles');
  await expect(detail.profileCheckbox('docker')).toBeChecked();
  await expect(detail.configOther).toHaveValue('');
  await detail.configCancel.click();
  await expect(detail.configModal).not.toHaveClass(/open/);
});

// An unrecognised profile is saved rather than blocked — the picker cannot
// enumerate every possible value — but the popup says so, because the failure
// would otherwise only surface when the next session starts.
test('an unknown profile is warned about but still saved', async ({
  page,
  tempProject,
}) => {
  const sessions = new SessionsPage(page);
  await sessions.goto();
  await sessions.createSession(tempProject.dir);

  const detail = new ProjectDetailPage(page);
  await detail.goto(tempProject.name);

  await detail.openEditor('sandbox_profiles');
  await detail.configOther.fill('not-a-real-profile');
  await expect(detail.configWarning).toBeVisible();
  await expect(detail.configWarning).toContainText('not-a-real-profile');

  await detail.saveEditor();
  await expect(detail.configValue('sandbox_profiles')).toHaveText(
    'not-a-real-profile',
  );

  // Reopening puts the unenumerated name back in the free-text field, so it
  // round-trips instead of being silently dropped.
  await detail.openEditor('sandbox_profiles');
  await expect(detail.configOther).toHaveValue('not-a-real-profile');
});

// Saving re-reads the whole page from the server instead of patching just the
// edited row: a project's configuration is not a set of independent cells (the
// server trims and normalises what it stores, and a session's effective
// settings are derived from several fields at once), so a page that keeps its
// pre-save render can go on displaying a configuration the server no longer
// holds. The stale marker below stands in for any such drift — only a re-render
// from the server can clear it.
test('saving a config field reloads the page values', async ({
  page,
  tempProject,
}) => {
  const sessions = new SessionsPage(page);
  await sessions.goto();
  await sessions.createSession(tempProject.dir);

  const detail = new ProjectDetailPage(page);
  await detail.goto(tempProject.name);

  // Plant a stale value in a row other than the one about to be edited.
  await detail
    .configValue('agent')
    .evaluate((el) => (el.textContent = 'STALE'));
  await expect(detail.configValue('agent')).toHaveText('STALE');

  await detail.openEditor('sandbox');
  await page.locator('#cfg-text').fill('none');
  await detail.saveEditor();

  await expect(detail.configValue('sandbox')).toHaveText('none');
  await expect(detail.configValue('agent')).not.toHaveText('STALE');
});

// A text field (agent) uses the plain single-line editor rather than checkboxes.
test('a plain text config field can be edited', async ({
  page,
  tempProject,
}) => {
  const sessions = new SessionsPage(page);
  await sessions.goto();
  await sessions.createSession(tempProject.dir);

  const detail = new ProjectDetailPage(page);
  await detail.goto(tempProject.name);

  await detail.openEditor('agent');
  await page.locator('#cfg-text').fill('some-agent --flag');
  await detail.saveEditor();

  await expect(detail.configValue('agent')).toHaveText('some-agent --flag');
  await page.reload();
  await expect(detail.configValue('agent')).toHaveText('some-agent --flag');
});
