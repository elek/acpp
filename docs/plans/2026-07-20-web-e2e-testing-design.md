# Web UI Browser E2E Testing — Design

**Date:** 2026-07-20
**Status:** Approved, ready for implementation

## Goal

A browser-based end-to-end test suite for `./web`, structured to grow: many
scenarios added over time, each driving a real browser against a real `acpp`
server backed by a real (empty-at-start) PostgreSQL database and a real ACP
agent.

## Decisions

- **Framework:** Playwright (TypeScript). Chosen over Go (chromedp/rod) for
  mainstream tooling (auto-wait, trace viewer, codegen).
- **Agent:** real agents only, run as a matrix — `rai` (real provider) and
  Claude Code ACP (`claude-code-acp`). No mock/fake agent: the suite exercises
  the real pipeline. Because output is non-deterministic, content assertions are
  tolerant (case-insensitive substring); UI/structural assertions stay strict.
- **Server lifecycle:** the Go server runs as a **subprocess** (Playwright can't
  host it in-process), started from global-setup, health-gated on `/api/health`.
- **Database:** dockerized PostgreSQL on an isolated port, **truncated to empty**
  at the start of each run — same guarantee as `integration/harness.go`.
- **Scenario 6 semantics:** loose — after `stop → new session`, assert only that
  a session starts and *some* response returns (content irrelevant, since a new
  session is a fresh agent context).
- **Step 5 (post-stop UI) is a strict behavioral assertion.** If the current UI
  does not match ("send/cancel gone, textarea remains, new-session button
  appears"), the **implementation is fixed**, not the test.

## Layout

```
web/e2e/
  package.json            # @playwright/test
  playwright.config.ts    # webServer/global-setup + agent matrix, timeouts, retries
  docker-compose.yml      # test postgres, off-default port (:5434)
  global-setup.ts         # build acpp, render config, truncate DB, start server, health-gate
  global-teardown.ts      # stop server, tear down docker
  tsconfig.json
  fixtures/               # tempProject, server (+ later: db)
  pages/                  # SessionsPage, ProjectPage (Page Object Model)
  tests/                  # one *.spec.ts per scenario
  README.md
```

Mirrors the `android/e2e/` convention (config-template rendering, isolated
`XDG_CONFIG_HOME`, off-default ports).

## Fixtures

- `tempProject` — fresh temp dir + `git init` + single `README` + initial
  commit; returns `{ dir, name }`; auto-cleaned. Implements scenario 1's project
  scaffolding, reusable across specs.
- `server` — running server base URL + active agent id (from matrix project).
- `db` (later) — direct row assertions when a test wants to verify persistence.

## Page Objects

Selectors defined once, as getters, so template changes touch one file.

- `SessionsPage` — `gotoSessions()`, `openNewSession()`, `createSession(dir, agent?)`
  driving the "+ New Session" modal (`list.html:130` — `#ns-dir`, `#ns-agent`,
  `#ns-submit`). This is how a brand-new project is bootstrapped from an empty DB
  (the project list derives from existing sessions, `db/store_projects.go:15`).
- `ProjectPage` (`/projects?project=X`, `projectview.html`) — the workhorse:
  - Elements: `promptInput` (`#prompt-input`), `send` (`#prompt-send`),
    `cancelButton` (`#prompt-cancel`), `stopButton` (`.stop-btn`),
    `conversation` (`#conversation`), `newSessionButton`.
  - Helpers: `goto(name)`, `send(text)`, `waitForAssistantResponse()`,
    `responseText()`, `stop()`, `expectStoppedState()`, `startNewSession()`.

All waits are Playwright web-first assertions on visibility/text — **never fixed
sleeps** — which is what keeps real-agent tests non-flaky.

## Scenario 1 spec

`tests/capital-of-hungary.spec.ts`:

1. `tempProject` creates dir + git + README.
2. `SessionsPage.createSession(tempProject.dir)` → establishes the project.
3. `ProjectPage.goto(name)` then `send('What is the capital of Hungary')`.
4. `waitForAssistantResponse()`; assert `responseText()` matches `/hungary|budapest/i`.
5. `stop()` (`.stop-btn`).
6. `expectStoppedState()` — send/cancel gone, textarea present, new-session
   button visible (**strict**; fix impl if it differs).
7. `startNewSession()`, `send('what was the previous question')`,
   `waitForAssistantResponse()`, assert a non-empty response (loose).

## Robustness / CI

- No fixed sleeps; generous per-test timeout for slow real turns.
- `retries: CI ? 2 : 0`; trace-on-first-retry + HTML report as CI artifacts.
- `rai` runs by default (provider key as CI secret); `claude-code-acp` is
  `test.skip`-gated on its auth/env so credential-less runs stay green.

## Commands

From `web/e2e/`:

- `npm run e2e` — full run (both agents); boots docker postgres + server.
- `npm run e2e -- --project=rai` — single agent.
- `npm run e2e:headed` / `--debug` — local debugging.

## Non-goals (YAGNI)

- No visual-regression snapshots.
- Chromium only (no cross-browser matrix).
- One truncated DB per run (no per-worker DB isolation yet).
</content>
</invoke>
