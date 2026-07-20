import { defineConfig, devices } from '@playwright/test';
import { AGENTS, baseURLForAgent } from './harness/env';
import { ensureFonts } from './harness/fonts';

// Runs in the main runner and in every worker (config is re-imported per
// process), so the browser each worker launches inherits FONTCONFIG_FILE.
ensureFonts();

// One Playwright project per agent (rai, claude-code-acp). global-setup starts a
// server per *available* agent; a project whose server was not started skips via
// the `server` fixture. Real agents are slow and non-deterministic, so timeouts
// are generous and CI retries twice.
export default defineConfig({
  testDir: './tests',
  globalSetup: require.resolve('./harness/global-setup'),
  globalTeardown: require.resolve('./harness/global-teardown'),
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  timeout: 120_000,
  expect: { timeout: 60_000 },
  reporter: [['html', { open: 'never' }], ['list']],
  use: {
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    // Use the full Chromium build, not chrome-headless-shell: the shell build
    // FATALs in Skia's font manager ("SkFontMgr_FontConfigInterface ... Not
    // implemented") on hosts without system fonts, crashing the renderer on any
    // page that selects a font-family.
    channel: 'chromium',
  },
  projects: AGENTS.map((agent) => ({
    name: agent.name,
    use: {
      ...devices['Desktop Chrome'],
      baseURL: baseURLForAgent(agent.name),
      channel: 'chromium',
    },
  })),
});
