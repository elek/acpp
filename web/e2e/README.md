# Web UI browser e2e tests

Playwright (TypeScript) end-to-end tests that drive the acpp web UI in a real
browser against a real `acpp` server, an empty PostgreSQL database and a real ACP
agent. Design: `docs/plans/2026-07-20-web-e2e-testing-design.md`.

## Prerequisites

- Node.js 18+ and `docker` (for postgres)
- Go (the harness builds `acpp` from the repo)
- At least one agent on `PATH`:
  - `rai` (run with a real provider — needs its provider credentials), or
  - `claude-code-acp` (needs Claude auth)

  A project whose agent binary is not on `PATH` is **skipped**, so a machine with
  neither agent runs green (everything skips).

## Install

```sh
cd web/e2e
npm install          # also installs the Chromium browser (postinstall)
```

## Run

```sh
npm run e2e                       # all available agents
npm run e2e -- --project=rai      # single agent
npm run e2e:headed                # watch it in a browser
npm run e2e:debug                 # Playwright inspector
npm run e2e:report                # open the last HTML report
```

The harness (`harness/global-setup.ts`) automatically:

1. brings up an **empty** postgres via `docker-compose.yml` (host port 5434),
2. builds `acpp` into `.run/bin/acpp`,
3. starts one `acpp serve` server per available agent (rai → :6071,
   claude-code-acp → :6072) with a private `XDG_CONFIG_HOME`,
4. health-gates each on `/api/health`.

`global-teardown.ts` stops the servers and drops the database.

## Agent selection / overrides

Each agent is one Playwright project. Override the launched command for local
smoke runs (no credentials) via env:

```sh
# boot with the deterministic offline fake agent (content assertions won't hold,
# but UI/flow assertions like the stop → new-session state do)
ACPP_E2E_RAI_AGENT="rai acp fake" npm run e2e -- --project=rai
```

- `ACPP_E2E_RAI_AGENT` (default `rai acp`)
- `ACPP_E2E_CLAUDE_AGENT` (default `claude-code-acp`)

## Layout

```
harness/     server + docker lifecycle, config rendering, shared env
fixtures/    tempProject (git repo + README), server (per-agent skip + baseURL)
pages/       Page Objects: SessionsPage, ProjectPage
tests/       one *.spec.ts per scenario
```

## Adding a scenario

Add a `tests/*.spec.ts` using the `tempProject` fixture and the page objects.
Add new selectors as getters on the relevant page object — never inline in the
spec — so the suite stays maintainable as it grows.
```
