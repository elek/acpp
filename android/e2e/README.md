# Android end-to-end test

A full-stack happy-path test for the Android client: a real device/emulator
drives the real app against a **separately started** acpp server backed by a
**dockerized postgres**, with **`rai acp fake`** as the ACP agent (deterministic,
no API keys).

```
e2e/
├── docker-compose.yml        # postgres:16 on host port 5433, ephemeral (tmpfs)
├── config/acpp/config.yaml   # server config TEMPLATE (rendered at start time)
├── projects/demo/            # working directory the test session runs in
├── _common.sh                # shared setup (db, build, config render, health)
├── start-backend.sh          # bring up the stack and leave it running (manual testing)
├── stop-backend.sh           # tear the stack down
└── run-e2e.sh                # automated: stack up → run instrumented test → tear down
```

The instrumented test lives at
`app/src/androidTest/java/net/anzix/acpp/e2e/ChatFlowE2ETest.kt`.

## What the test does

1. Setup screen → enter server URL → **Connect** → Projects → **New** session in
   `projects/demo` → type `hello` → **Send** → wait for the agent's reply bubble.
2. Overflow menu → **Clear** (sends `/clear`, so the backend replaces the
   session and the UI navigates to a fresh, empty conversation) → type
   `[scenario1 count=5]` → **Send**.

`rai acp fake` emits *random* words for a plain prompt, so those assertions are
**structural** (the user message echoes, a non-empty assistant bubble appears) —
never on the reply text.

The `[scenario1 count=5]` directive is **deterministic**: the fake agent runs 5
iterations, each performing a `create` then a `cat` tool call. The second
conversation therefore asserts the exact shape — precisely 10 tool-call rows in
`create, cat, …` order, all rendered below the single user prompt.

## Prerequisites

- `docker` + `docker compose`, `go`, `rai` (on `PATH`), `adb`
- A connected device or running emulator (`adb devices`)
- Android SDK for the Gradle build

## Run it (automated)

```bash
# Default — works for an emulator OR a USB-connected real phone. An `adb
# reverse` tunnel forwards the device's 127.0.0.1:6061 back to the host server,
# and the device is woken + unlocked (non-secure keyguard only) first:
android/e2e/run-e2e.sh

# Alternative — talk to the host directly over the LAN (phone on the same
# network; firewall must allow :6061), skipping the tunnel:
HOST_IP=192.168.1.42 android/e2e/run-e2e.sh
```

Report: `android/app/build/reports/androidTests/connected/debug/index.html`.

### Optional knobs

```bash
# Watch it run: pause ~1s after each UI step so a human can follow on-device.
STEP_DELAY_MS=1000 android/e2e/run-e2e.sh

# Keep the backend (postgres + acpp web + the adb reverse tunnel) up after the
# test so you can keep poking at it by hand. Tear it down when done:
KEEP_RUNNING=1 android/e2e/run-e2e.sh
android/e2e/stop-backend.sh

# Combine them — slow, observable run that leaves the stack up afterwards:
STEP_DELAY_MS=1000 KEEP_RUNNING=1 android/e2e/run-e2e.sh
```

## Manual testing

```bash
android/e2e/start-backend.sh     # leaves acpp web running on :6061
# ... drive the app by hand. Point it at:
#   emulator:           http://10.0.2.2:6061
#   real phone (LAN):   http://<host-LAN-IP>:6061
#   real phone (USB):   adb reverse tcp:6061 tcp:6061, then http://127.0.0.1:6061
# Use projects/demo as the session dir.
android/e2e/stop-backend.sh
```

## Isolation

- postgres on **:5433** (not 5432), data in tmpfs — fresh schema every run
- acpp on **:6061** (not the usual :6060)
- config rendered into `.run/` and loaded via `XDG_CONFIG_HOME`, so your real
  `~/.config/acpp` is untouched
- `sandbox: none` — the agent runs directly (no bwrap)
