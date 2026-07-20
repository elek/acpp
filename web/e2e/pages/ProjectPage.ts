import { Page, Locator, expect } from '@playwright/test';

// ProjectPage drives the /projects?project=<name> view: the prompt bar, the
// stop button and the streamed conversation. Selectors are defined once here so
// template changes touch a single file.
export class ProjectPage {
  readonly promptInput: Locator;
  readonly sendButton: Locator;
  readonly cancelButton: Locator;
  readonly newSessionButton: Locator;
  readonly stopButton: Locator;
  readonly conversation: Locator;
  readonly assistantMessages: Locator;
  readonly separators: Locator;

  constructor(private readonly page: Page) {
    this.promptInput = page.locator('#prompt-input');
    this.sendButton = page.locator('#prompt-send');
    this.cancelButton = page.locator('#prompt-cancel');
    this.newSessionButton = page.locator('#prompt-new-session');
    this.stopButton = page.locator('#stop-btn');
    this.conversation = page.locator('#conversation');
    this.assistantMessages = page.locator('#conversation .msg-assistant .msg-content');
    this.separators = page.locator('#conversation .prompt-separator');
  }

  async goto(project: string): Promise<void> {
    await this.page.goto(`/projects?project=${encodeURIComponent(project)}`);
    await expect(this.promptInput).toBeVisible();
  }

  // send types a prompt and submits it to the active running session. It records
  // the finished-turn count first so waitForResponse can detect this turn's
  // completion even across multiple turns.
  async send(text: string): Promise<void> {
    this.turnsBefore = await this.separators.count();
    // Simulated keystrokes/fill do not reliably reach inputs under headless
    // Chromium; set the value directly (the send handler reads .value).
    await this.promptInput.evaluate((el, v) => {
      const ta = el as HTMLTextAreaElement;
      ta.value = v;
      ta.dispatchEvent(new Event('input', { bubbles: true }));
    }, text);
    await expect(this.promptInput).toHaveValue(text);
    await this.sendButton.click();
  }

  private turnsBefore = 0;

  // waitForResponse blocks until the turn started by the last send() completes
  // (a new prompt-separator is appended on prompt_finished).
  async waitForResponse(): Promise<void> {
    await expect
      .poll(() => this.separators.count(), { timeout: 90_000 })
      .toBeGreaterThan(this.turnsBefore);
  }

  // responseText returns the trimmed text of the most recent assistant message.
  async responseText(): Promise<string> {
    return (await this.assistantMessages.last().innerText()).trim();
  }

  async stop(): Promise<void> {
    await this.stopButton.click();
  }

  // expectStoppedState asserts the post-stop prompt bar: Send and Cancel are
  // gone, the text area remains, and a "New session" button is offered.
  async expectStoppedState(): Promise<void> {
    await expect(this.newSessionButton).toBeVisible();
    await expect(this.sendButton).toBeHidden();
    await expect(this.cancelButton).toBeHidden();
    await expect(this.promptInput).toBeVisible();
  }

  // startNewSession clicks "New session" and waits for the prompt bar to return
  // to normal Send mode against the fresh session.
  async startNewSession(): Promise<void> {
    await this.newSessionButton.click();
    await expect(this.sendButton).toBeVisible();
    await expect(this.newSessionButton).toBeHidden();
  }
}
