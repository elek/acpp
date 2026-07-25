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

  constructor(private readonly page: Page) {
    this.heading = page.locator('main h1');
    this.subtitle = page.locator('main .subtitle');
    this.cards = page.locator('.scard');
    this.activeCards = page.locator('#active-grid .scard');
    this.closedCards = page.locator('#closed-grid .scard');
    this.cardOpenLinks = page.locator('.scard .scard-open');
    this.configTable = page.locator('.cfg-table');
    this.navWorkplace = page.locator('.nav-link', { hasText: 'Workplace' });
  }

  async goto(project: string): Promise<void> {
    await this.page.goto(`/project/${encodeURIComponent(project)}`);
    await expect(this.heading).toBeVisible();
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
