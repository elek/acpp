import { test, expect } from '../fixtures';
import { ProjectPage } from '../pages/ProjectPage';

// The /projects bottom tab bar offers a "+" button that opens a modal to create
// a new project. Creating one with a name (and an explicit directory) persists a
// project row, navigates to its view — which opens on a freshly started pending
// session — and lists it in the tab bar.
test('new-project button creates a project and navigates to it', async ({
  page,
  tempProject,
}) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();

  await project.createProject(tempProject.name, tempProject.dir);

  // The URL now targets the new project and the tab bar lists it.
  expect(new URL(page.url()).searchParams.get('project')).toBe(tempProject.name);
  await expect(project.projectTitle).toHaveText(tempProject.name);
  await expect(
    project.projectTabs.filter({ hasText: tempProject.name }),
  ).toHaveCount(1);

  // Reloading the bare list keeps the project — it was persisted, not just added
  // to the DOM.
  await project.gotoProjects();
  await expect(
    project.projectTabs.filter({ hasText: tempProject.name }),
  ).toHaveCount(1);
});

// A directory that does not exist is rejected: the server returns 400 and the
// modal shows the error inline instead of navigating.
test('new-project rejects a non-existent directory', async ({ page }) => {
  const project = new ProjectPage(page);
  await project.gotoProjects();

  await project.newProjectButton.click();
  await expect(project.newProjectModal).toHaveClass(/open/);
  await project.newProjectName.evaluate((el) => {
    (el as HTMLInputElement).value = 'ghost-project';
  });
  await project.newProjectDir.evaluate((el) => {
    const input = el as HTMLInputElement;
    input.value = '/no/such/dir/anywhere';
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await project.newProjectCreate.click();

  // Stays on the modal with a visible error; no navigation happened.
  await expect(project.newProjectError).toBeVisible();
  await expect(project.newProjectModal).toHaveClass(/open/);
  expect(new URL(page.url()).searchParams.get('project')).toBeNull();
});
