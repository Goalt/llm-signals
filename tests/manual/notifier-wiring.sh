#!/usr/bin/env bash
# Manual wiring test for cmd/server notifier env variables.
#
# For each scenario we start the binary with a crafted env, read stdout for
# 1.5s, then kill the process. We assert the startup log contains (or does
# NOT contain) expected lines. The server binary is expected at /tmp/tg-server.
set -u

BIN="${BIN:-/tmp/tg-server}"
LOG="${LOG:-tests/manual/notifier-wiring.log}"
mkdir -p "$(dirname "$LOG")"
: > "$LOG"

PORT_BASE=18100

pass=0
fail=0
log() { echo "$@" | tee -a "$LOG"; }

# run_case NAME EXPECT_REGEX DENY_REGEX -- env=val... --
# EXPECT_REGEX may contain multiple patterns separated by "&&"; all must match.
run_case() {
  local name="$1" expect="$2" deny="$3"; shift 3
  shift # consume --
  local envs=()
  while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do envs+=("$1"); shift; done
  PORT_BASE=$((PORT_BASE + 1))
  local out
  out=$(timeout --preserve-status -s KILL -k 0.2 1.5 env -i HOME="$HOME" PATH="$PATH" \
      PORT="$PORT_BASE" HOST=127.0.0.1 "${envs[@]}" "$BIN" 2>&1 || true)

  log ""
  log "── $name ──"
  log "env: ${envs[*]}"
  log "$out"

  local ok=1
  if [ -n "$expect" ]; then
    IFS='&' read -r -a patterns <<<"$expect"
    for p in "${patterns[@]}"; do
      # Skip empty tokens from "&&" separator
      [ -z "$p" ] && continue
      if ! grep -Eq "$p" <<<"$out"; then
        log "FAIL: expected to match /$p/"
        ok=0
      fi
    done
  fi
  if [ -n "$deny" ] && grep -Eq "$deny" <<<"$out"; then
    log "FAIL: expected NOT to match /$deny/"
    ok=0
  fi
  if [ "$ok" -eq 1 ]; then
    log "PASS: $name"
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
  fi
}

# 1) No env → both notifiers disabled.
run_case "no env: both notifiers disabled" \
  "notifier disabled.*TG_CHANNELS.*" \
  "^notifier: polling|^x.com notifier: polling" -- --

# 2) TG channels + webhooks → Telegram notifier enabled.
run_case "TG channels + WEBHOOKS: tg notifier enabled" \
  "notifier: polling 2 channel\(s\) every 10s.*2 webhook" \
  "^notifier disabled" -- \
  TG_CHANNELS=durov,telegram WEBHOOKS=http://127.0.0.1:9/a,http://127.0.0.1:9/b POLL_INTERVAL=10s --

# 3) X_USERS set but no bearer token → x.com notifier disabled explicitly.
run_case "X_USERS without X_BEARER_TOKEN: x notifier disabled" \
  "x.com notifier disabled: set X_BEARER_TOKEN" \
  "^x.com notifier: polling" -- \
  X_USERS=jack WEBHOOKS=http://127.0.0.1:9/a --

# 4) X_USERS + token + webhooks → x.com stream startup is attempted.
run_case "X_USERS + token + WEBHOOKS: x stream startup attempted" \
  "x.com stream: start failed" \
  "^x.com notifier disabled" -- \
  X_USERS=jack X_BEARER_TOKEN=fake-token WEBHOOKS=http://127.0.0.1:9/a X_POLL_INTERVAL=10s --

# 5) Both notifiers enabled simultaneously.
run_case "TG + X both enabled simultaneously" \
  "notifier: polling 1 channel&&x.com stream: start failed" \
  "notifier disabled" -- \
  TG_CHANNELS=durov X_USERS=jack X_BEARER_TOKEN=fake-token \
  WEBHOOKS=http://127.0.0.1:9/a POLL_INTERVAL=10s X_POLL_INTERVAL=10s --

# 6) Invalid POLL_INTERVAL → fatal exit.
run_case "invalid POLL_INTERVAL: fatal" \
  "invalid POLL_INTERVAL" "" -- \
  TG_CHANNELS=durov WEBHOOKS=http://127.0.0.1:9/a POLL_INTERVAL=not-a-duration --

# 7) Invalid X_POLL_INTERVAL → fatal exit.
run_case "invalid X_POLL_INTERVAL: fatal" \
  "invalid X_POLL_INTERVAL" "" -- \
  X_USERS=jack X_BEARER_TOKEN=t WEBHOOKS=http://127.0.0.1:9/a X_POLL_INTERVAL=bogus --

log ""
log "=========================="
log "Wiring summary: PASS=$pass FAIL=$fail"
log "=========================="
exit "$fail"
