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
  readonly stopForm: Locator;
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
  readonly noProjectState: Locator;
  readonly infoButton: Locator;
  readonly infoPanel: Locator;
  readonly infoClose: Locator;
  readonly infoCost: Locator;
  readonly infoContextCount: Locator;
  readonly infoContextPct: Locator;

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
    // The Stop button's form — its `action` must always point at the session
    // currently on screen, which matters when a session is swapped in-place.
    this.stopForm = page.locator('#stop-form');
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
    // The bare desktop with no project window open.
    this.noProjectState = page.locator('#no-project');
    // Session Info side panel: the toggle in the session bar, the panel itself and
    // its live cost / context-window figures.
    this.infoButton = page.locator('#info-btn');
    this.infoPanel = page.locator('#session-info');
    this.infoClose = page.locator('#info-close');
    this.infoCost = page.locator('#info-cost');
    this.infoContextCount = page.locator('#info-ctx-count');
    this.infoContextPct = page.locator('#info-ctx-pct');
  }

  // openInfoPanel clicks the session-bar info toggle and waits for the panel to
  // become visible. Opening also triggers an authoritative /api refresh, but the
  // live-streamed usage_update is what these tests assert against.
  async openInfoPanel(): Promise<void> {
    if (await this.infoPanel.isVisible()) return;
    await this.infoButton.click();
    await expect(this.infoPanel).toBeVisible();
  }

  async goto(project: string): Promise<void> {
    await this.page.goto(`/projects?project=${encodeURIComponent(project)}`);
    await expect(this.promptInput).toBeVisible();
  }

  // gotoSession opens one specific session of a project — the full page load a
  // taskbar session dot performs.
  async gotoSession(project: string, session: string): Promise<void> {
    await this.page.goto(
      `/projects?project=${encodeURIComponent(project)}&session=${encodeURIComponent(session)}`,
    );
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

  // typeDraft fills the prompt box WITHOUT sending, then waits until the draft
  // has actually been written to localStorage. Saving is debounced, so a test
  // that navigated immediately would be racing the write it means to verify.
  async typeDraft(text: string, key: string): Promise<void> {
    await this.promptInput.evaluate((el, v) => {
      const ta = el as HTMLTextAreaElement;
      ta.value = v;
      ta.dispatchEvent(new Event('input', { bubbles: true }));
    }, text);
    await expect(this.promptInput).toHaveValue(text);
    await expect.poll(() => this.storedDraft(key)).toBe(text);
  }

  // seedDraft writes a draft entry directly, for states that are awkward to
  // reach by typing — notably a draft saved under the project key, which only
  // happens while a project window has no session yet.
  async seedDraft(key: string, text: string): Promise<void> {
    await this.page.evaluate(
      ({ k, t }) => {
        const raw = localStorage.getItem('acpp.promptDrafts');
        const drafts = raw ? JSON.parse(raw) : {};
        drafts[k] = { text: t, savedAt: Date.now() };
        localStorage.setItem('acpp.promptDrafts', JSON.stringify(drafts));
      },
      { k: key, t: text },
    );
  }

  // storedDraft reads one entry out of the persisted draft blob, so tests can
  // assert on the storage contract itself and not only on what the box shows.
  async storedDraft(key: string): Promise<string> {
    return this.page.evaluate((k) => {
      try {
        const raw = localStorage.getItem('acpp.promptDrafts');
        if (!raw) return '';
        const entry = JSON.parse(raw)[k];
        return entry && typeof entry.text === 'string' ? entry.text : '';
      } catch {
        return '';
      }
    }, key);
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

  // pasteImageViaAsyncClipboard simulates the ACTUAL behaviour of WebKitGTK (the
  // Linux desktop wrapper's engine): on image paste it drops image MIME types
  // from the synchronous paste event, so clipboardData.items AND .files are both
  // empty (WebKit bug 218519). The image is reachable only through the async
  // Clipboard API. This is the case pasteImageViaFiles does NOT cover — .files is
  // also empty here — and is the real "paste works in the webapp but not the
  // desktop app" reproduction. We stub navigator.clipboard.read to stand in for
  // the system clipboard and dispatch an empty paste event.
  async pasteImageViaAsyncClipboard(mimeType: string, base64: string): Promise<void> {
    const before = await this.attachmentThumbs.count();
    await this.promptInput.evaluate((el, args) => {
      const bin = atob(args.base64);
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      const blob = new Blob([bytes], { type: args.mimeType });
      const stub = () => Promise.resolve([{ types: [args.mimeType], getType: () => Promise.resolve(blob) }]);
      try {
        Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { read: stub } });
      } catch {
        (navigator.clipboard as unknown as { read: () => Promise<unknown> }).read = stub;
      }
      // WebKitGTK-style paste event: image absent from both .items and .files.
      const ev = new Event('paste', { bubbles: true, cancelable: true });
      Object.defineProperty(ev, 'clipboardData', { value: { items: [], files: [] } });
      el.dispatchEvent(ev);
    }, { mimeType, base64 });
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
  // from the session-bar "new" button instead. Only meaningful while the window
  // is still open — i.e. the project has another session live; when the stopped
  // one was the last, use expectWindowClosed instead.
  async expectStoppedState(): Promise<void> {
    await expect(this.promptBar).toBeHidden();
  }

  // expectWindowClosed asserts the project's window is gone: the browser is back
  // on the bare desktop (no project in the URL, so nothing is re-opened and no
  // new session is started), the session and prompt bars went with it, and the
  // project has no taskbar tab left.
  async expectWindowClosed(project: string): Promise<void> {
    await this.page.waitForURL(
      (url) => url.pathname === '/projects' && !url.searchParams.get('project'),
      { timeout: 30_000 },
    );
    await expect(this.noProjectState).toBeVisible();
    await expect(this.promptBar).toHaveCount(0);
    await expect(this.projectTabs.filter({ hasText: project })).toHaveCount(0);
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
