import { Page, Locator, expect } from '@playwright/test';

// ProjectDetailPage drives the /project/<name> overview page: the project's
// configuration table plus its sessions rendered as cards, split into an Active
// and a Closed grid. It is reached by clicking a project's name in the session
// view. Selectors live here so template changes touch a single file.
export class ProjectDetailPage {
  readonly heading: Locator;
  readonly subtitle: Locator;
  readonly cards: Locator;
  readonly activeCards: Locator;
  readonly closedCards: Locator;
  readonly cardOpenLinks: Locator;
  readonly configTable: Locator;
  readonly navWorkplace: Locator;
  readonly configModal: Locator;
  readonly configOther: Locator;
  readonly configWarning: Locator;
  readonly configSave: Locator;
  readonly configCancel: Locator;

  constructor(private readonly page: Page) {
    this.heading = page.locator('main h1');
    this.subtitle = page.locator('main .subtitle');
    this.cards = page.locator('.scard');
    this.activeCards = page.locator('#active-grid .scard');
    this.closedCards = page.locator('#closed-grid .scard');
    this.cardOpenLinks = page.locator('.scard .scard-open');
    this.configTable = page.locator('.cfg-table');
    this.navWorkplace = page.locator('.nav-link', { hasText: 'Workplace' });
    this.configModal = page.locator('#cfgModal');
    this.configOther = page.locator('#cfg-other');
    this.configWarning = page.locator('#cfg-warn');
    this.configSave = page.locator('#cfg-save');
    this.configCancel = page.locator('#cfg-cancel');
  }

  async goto(project: string): Promise<void> {
    await this.page.goto(`/project/${encodeURIComponent(project)}`);
    await expect(this.heading).toBeVisible();
  }

  // configValue is the rendered value cell for one config field.
  configValue(field: string): Locator {
    return this.page.locator(`tr[data-row="${field}"] .cfg-value`);
  }

  // openEditor clicks a field's pencil and waits for the popup. The button only
  // becomes visible on row hover, so click through the locator rather than
  // asserting visibility first.
  async openEditor(field: string): Promise<void> {
    await this.page.locator(`.cfg-edit[data-field="${field}"]`).click();
    await expect(this.configModal).toHaveClass(/open/);
  }

  // profileCheckbox is one entry in the profiles/hooks checkbox list.
  profileCheckbox(name: string): Locator {
    return this.page.locator(`#cfg-checklist input[value="${name}"]`);
  }

  async saveEditor(): Promise<void> {
    await this.configSave.click();
    await expect(this.configModal).not.toHaveClass(/open/);
  }

  // openFirstCard clicks the first card's "Open →" link and waits for the
  // browser to land back on that session's project view.
  async openFirstCard(): Promise<void> {
    await this.cardOpenLinks.first().click();
    await this.page.waitForURL((url) => url.pathname === '/projects', {
      timeout: 30_000,
    });
  }
}
