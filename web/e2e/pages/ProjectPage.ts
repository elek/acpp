import { Page, Locator, expect } from '@playwright/test';

// ProjectPage drives the /projects?project=<name> view: the prompt bar, the
// stop button and the streamed conversation. Selectors are defined once here so
// template changes touch a single file.
export class ProjectPage {
  readonly promptInput: Locator;
  readonly sendButton: Locator;
  readonly cancelButton: Locator;
  readonly newSessionButton: Locator;
  readonly newConversationButton: Locator;
  readonly stopButton: Locator;
  readonly sessionStatus: Locator;
  readonly sessionSelect: Locator;
  readonly conversation: Locator;
  readonly assistantMessages: Locator;
  readonly separators: Locator;

  constructor(private readonly page: Page) {
    this.promptInput = page.locator('#prompt-input');
    this.sendButton = page.locator('#prompt-send');
    this.cancelButton = page.locator('#prompt-cancel');
    this.newSessionButton = page.locator('#prompt-new-session');
    // The always-visible "new" conversation button in the top session bar
    // (distinct from #prompt-new-session, which only appears once a session is
    // stopped).
    this.newConversationButton = page.locator('#new-conversation-btn');
    this.stopButton = page.locator('#stop-btn');
    this.sessionStatus = page.locator('.session-bar .session-status');
    this.sessionSelect = page.locator('#session-select');
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

  // currentSessionId reads the `session` query param from the browser URL, which
  // the client rewrites (history.pushState) whenever the active session changes.
  currentSessionId(): string | null {
    return new URL(this.page.url()).searchParams.get('session');
  }

  // startNewConversation clicks the always-present session-bar "new" button and
  // waits for the client to navigate to a fresh session (a `session` query param
  // that differs from previousId). Returns the new session id.
  async startNewConversation(previousId: string | null): Promise<string> {
    await this.newConversationButton.click();
    await this.page.waitForFunction(
      (prev) => {
        const s = new URL(location.href).searchParams.get('session');
        return !!s && s !== prev;
      },
      previousId,
      { timeout: 30_000 },
    );
    const id = this.currentSessionId();
    if (!id) throw new Error('no session id in URL after starting a new conversation');
    return id;
  }

  // selectedSessionLabel returns the visible text of the currently-selected
  // option in the session dropdown (e.g. "Jul 20 15:04 — pending").
  async selectedSessionLabel(): Promise<string> {
    return (
      await this.sessionSelect.evaluate((el) => {
        const sel = el as HTMLSelectElement;
        return sel.options[sel.selectedIndex]?.text ?? '';
      })
    ).trim();
  }

  // reloadAndExpectStatus reloads the page and asserts the server-rendered
  // session-bar status. It retries the whole reload because the persisted status
  // is flushed asynchronously (e.g. "running" only lands after the turn's
  // PromptResponse is written), so a single reload can race the DB write.
  async reloadAndExpectStatus(status: string): Promise<void> {
    await expect(async () => {
      await this.page.reload();
      await expect(this.sessionStatus).toHaveText(status, { timeout: 5_000 });
    }).toPass({ timeout: 60_000 });
  }
}
