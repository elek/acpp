import { Page, Locator, expect } from '@playwright/test';

// ProjectPage drives the /projects?project=<name> view: the prompt bar, the
// stop button and the streamed conversation. Selectors are defined once here so
// template changes touch a single file.
export class ProjectPage {
  readonly promptInput: Locator;
  readonly promptBar: Locator;
  readonly sendButton: Locator;
  readonly cancelButton: Locator;
  readonly newConversationButton: Locator;
  readonly stopButton: Locator;
  readonly sessionStatus: Locator;
  readonly sessionSelect: Locator;
  readonly conversation: Locator;
  readonly assistantMessages: Locator;
  readonly errorMessages: Locator;
  readonly separators: Locator;
  readonly commandEchoes: Locator;
  readonly commandResponses: Locator;
  readonly fileInput: Locator;
  readonly attachmentThumbs: Locator;
  readonly userImages: Locator;
  readonly newProjectButton: Locator;
  readonly newProjectModal: Locator;
  readonly newProjectName: Locator;
  readonly newProjectDir: Locator;
  readonly newProjectCreate: Locator;
  readonly newProjectError: Locator;
  readonly projectTabs: Locator;
  readonly projectTitle: Locator;

  constructor(private readonly page: Page) {
    this.promptInput = page.locator('#prompt-input');
    this.promptBar = page.locator('#prompt-bar');
    this.sendButton = page.locator('#prompt-send');
    this.cancelButton = page.locator('#prompt-cancel');
    this.fileInput = page.locator('#prompt-file');
    this.attachmentThumbs = page.locator('#prompt-attachments .prompt-attachment');
    this.userImages = page.locator('#conversation .msg-user .msg-content img');
    // The always-visible "new" conversation button in the top session bar. Once a
    // session finishes, the prompt bar is hidden entirely and this is the only way
    // to start a fresh session.
    this.newConversationButton = page.locator('#new-conversation-btn');
    this.stopButton = page.locator('#stop-btn');
    this.sessionStatus = page.locator('.session-bar .session-status');
    this.sessionSelect = page.locator('#session-select');
    this.conversation = page.locator('#conversation');
    this.assistantMessages = page.locator('#conversation .msg-assistant .msg-content');
    // Harness-originated error block (e.g. the ACP session could not be created).
    this.errorMessages = page.locator('#conversation .msg-error .msg-content');
    this.separators = page.locator('#conversation .prompt-separator');
    this.commandEchoes = page.locator('#conversation .msg-command .msg-content');
    this.commandResponses = page.locator('#conversation .msg-command-response .msg-content');
    // New-project modal, opened from the "+" button in the bottom tab bar.
    this.newProjectButton = page.locator('#new-project-btn');
    this.newProjectModal = page.locator('#np-overlay');
    this.newProjectName = page.locator('#np-name');
    this.newProjectDir = page.locator('#np-dir');
    this.newProjectCreate = page.locator('#np-create');
    this.newProjectError = page.locator('#np-error');
    this.projectTabs = page.locator('.taskbar .taskbar-tab-name');
    this.projectTitle = page.locator('.session-bar .project-title');
  }

  async goto(project: string): Promise<void> {
    await this.page.goto(`/projects?project=${encodeURIComponent(project)}`);
    await expect(this.promptInput).toBeVisible();
  }

  // gotoProjects opens the bare project list (no active project). The bottom tab
  // bar and its "+" new-project button are always present; the prompt bar is not.
  async gotoProjects(): Promise<void> {
    await this.page.goto('/projects');
    await expect(this.newProjectButton).toBeVisible();
  }

  // createProject opens the new-project modal, fills the name (and optional dir),
  // submits, and waits for the client to navigate to the new project's view.
  async createProject(name: string, dir?: string): Promise<void> {
    await this.newProjectButton.click();
    await expect(this.newProjectModal).toHaveClass(/open/);
    // Simulated keystrokes/fill do not reliably reach these inputs under headless
    // Chromium; assign values directly (the submit handler reads .value).
    await this.setInputValue(this.newProjectName, name);
    if (dir !== undefined) {
      await this.setInputValue(this.newProjectDir, dir);
    }
    await this.newProjectCreate.click();
    await this.page.waitForURL(
      (url) => url.searchParams.get('project') === name,
      { timeout: 30_000 },
    );
  }

  private async setInputValue(locator: Locator, value: string): Promise<void> {
    await locator.evaluate((el, v) => {
      const input = el as HTMLInputElement;
      input.value = v;
      input.dispatchEvent(new Event('input', { bubbles: true }));
    }, value);
    await expect(locator).toHaveValue(value);
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

  // attachImage stages an image via the hidden file input (the same code path as
  // paste/drop) and waits for its thumbnail to render before returning.
  async attachImage(name: string, mimeType: string, buffer: Buffer): Promise<void> {
    const before = await this.attachmentThumbs.count();
    await this.fileInput.setInputFiles({ name, mimeType, buffer });
    await expect.poll(() => this.attachmentThumbs.count()).toBeGreaterThan(before);
  }

  // pasteImageViaFiles simulates a clipboard paste where the image is exposed
  // ONLY through clipboardData.files and NOT clipboardData.items — the behaviour
  // of WebKit engines (WebKitGTK on Linux, WKWebView on macOS) used by native
  // desktop wrappers. Chromium (the browser "webapp") populates .items instead,
  // so this path is what desktop apps exercise and browsers never do.
  async pasteImageViaFiles(name: string, mimeType: string, base64: string): Promise<void> {
    const before = await this.attachmentThumbs.count();
    await this.promptInput.evaluate((el, args) => {
      const bin = atob(args.base64);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      const file = new File([bytes], args.name, { type: args.mimeType });
      const ev = new Event('paste', { bubbles: true, cancelable: true });
      // WebKit-style clipboard: image reachable via .files, .items empty.
      Object.defineProperty(ev, 'clipboardData', { value: { items: [], files: [file] } });
      el.dispatchEvent(ev);
    }, { name, mimeType, base64 });
    await expect.poll(() => this.attachmentThumbs.count()).toBeGreaterThan(before);
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

  // expectStoppedState asserts that a finished session offers no way to send a
  // new prompt: the entire prompt bar is hidden. Starting a fresh session is done
  // from the session-bar "new" button instead.
  async expectStoppedState(): Promise<void> {
    await expect(this.promptBar).toBeHidden();
  }

  // startNewSession starts a fresh session via the session-bar "new" button (the
  // finished session's prompt bar is hidden) and waits for the prompt bar to
  // return in Send mode against the new session.
  async startNewSession(): Promise<void> {
    await this.newConversationButton.click();
    await expect(this.promptBar).toBeVisible();
    await expect(this.sendButton).toBeVisible();
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
