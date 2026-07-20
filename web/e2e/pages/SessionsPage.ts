import { Page, Locator, expect } from '@playwright/test';

// SessionsPage drives the /sessions list and its "New Session" modal — the only
// browser path that creates a session (and therefore a project) from an
// arbitrary working directory against an empty database.
export class SessionsPage {
  readonly newSessionButton: Locator;
  readonly modal: Locator;
  readonly dirInput: Locator;
  readonly agentInput: Locator;
  readonly submitButton: Locator;

  constructor(private readonly page: Page) {
    this.newSessionButton = page.getByRole('button', { name: '+ New Session' });
    this.modal = page.locator('#newSessionModal');
    this.dirInput = page.locator('#ns-dir');
    this.agentInput = page.locator('#ns-agent');
    this.submitButton = page.locator('#ns-submit');
  }

  async goto(): Promise<void> {
    await this.page.goto('/sessions');
  }

  // setValue assigns an input's value and fires an input event. Simulated
  // keystrokes/fill do not reliably reach this modal's fields under headless
  // Chromium, and the submit handler reads .value directly, so a DOM assignment
  // is both sufficient and deterministic.
  private async setValue(locator: Locator, value: string): Promise<void> {
    await locator.evaluate((el, v) => {
      const input = el as HTMLInputElement;
      input.value = v;
      input.dispatchEvent(new Event('input', { bubbles: true }));
    }, value);
    await expect(locator).toHaveValue(value);
  }

  // createSession opens the modal, submits the given directory (the agent is
  // prefilled from the server defaults) and waits for the redirect to the new
  // session page. Returns the created session id.
  async createSession(dir: string): Promise<string> {
    await this.newSessionButton.click();
    await expect(this.modal).toHaveClass(/open/);
    await this.setValue(this.dirInput, dir);
    await this.submitButton.click();
    await this.page.waitForURL(/\/session\/[^/]+/, { timeout: 30_000 });
    const match = /\/session\/([^/?]+)/.exec(this.page.url());
    if (!match) throw new Error(`unexpected URL after create: ${this.page.url()}`);
    return match[1];
  }
}
