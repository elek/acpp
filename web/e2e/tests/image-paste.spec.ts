import { test, expect } from '../fixtures';
import { SessionsPage } from '../pages/SessionsPage';
import { ProjectPage } from '../pages/ProjectPage';

// A 1x1 red PNG — the smallest valid image to stage and round-trip.
const RED_PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==',
  'base64',
);

// Scenario: stage an image on the prompt bar (via the file input, the same path
// paste/drop feed), send it with text, and verify the user's turn echoes the
// image back — the deterministic, frontend-only signal that the image made the
// round trip. The agent's reply is asserted only loosely (vision support and
// wording are non-deterministic).
test('attach an image with text and see it echoed', async ({ page, tempProject }) => {
  const sessions = new SessionsPage(page);
  await sessions.goto();
  await sessions.createSession(tempProject.dir);

  const project = new ProjectPage(page);
  await project.goto(tempProject.name);

  // Stage the image, then type text and send both together.
  await project.attachImage('red.png', 'image/png', RED_PNG);
  await project.send('What color is this image?');

  // The user turn should render the pasted image (data URL round-tripped through
  // the backend echo).
  await expect(project.userImages.last()).toBeVisible();
  const src = await project.userImages.last().getAttribute('src');
  expect(src).toMatch(/^data:image\/png;base64,/);

  // The staging strip is cleared once sent.
  await expect(project.attachmentThumbs).toHaveCount(0);

  // A turn completes with some answer (content left loose).
  await project.waitForResponse();
  expect((await project.responseText()).length).toBeGreaterThan(0);
});
