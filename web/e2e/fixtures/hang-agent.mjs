#!/usr/bin/env node
// A minimal ACP agent that completes the handshake, starts a turn, then hangs:
// it streams one chunk of output and never sends the session/prompt response.
// This holds the conversation in its in-progress ("running") state indefinitely,
// which is exactly the window the persisted-status test needs to observe — a
// real or fake agent finishes its turn too fast to reliably reload into.
//
// Speaks line-delimited JSON-RPC 2.0 over stdio (see acp/connection.go).
import * as readline from 'node:readline';

const SESSION_ID = 'hang-1';

function write(obj) {
  process.stdout.write(JSON.stringify(obj) + '\n');
}

function result(id, res) {
  write({ jsonrpc: '2.0', id, result: res });
}

const rl = readline.createInterface({ input: process.stdin });
rl.on('line', (line) => {
  const text = line.trim();
  if (!text) return;
  let msg;
  try {
    msg = JSON.parse(text);
  } catch {
    return;
  }
  const { id, method } = msg;

  switch (method) {
    case 'initialize':
      result(id, { protocolVersion: 1 });
      break;
    case 'session/new':
      result(id, { sessionId: SESSION_ID });
      break;
    case 'session/load':
      result(id, {});
      break;
    case 'authenticate':
      result(id, {});
      break;
    case 'session/prompt':
      // Emit one chunk so a turn is visibly underway, then deliberately never
      // respond to this request: the turn stays in progress forever.
      write({
        jsonrpc: '2.0',
        method: 'session/update',
        params: {
          sessionId: SESSION_ID,
          update: {
            sessionUpdate: 'agent_message_chunk',
            content: { type: 'text', text: 'thinking...' },
          },
        },
      });
      // Stream a usage_update mid-turn (before any prompt response). This is the
      // window the Session Info panel's live cost/context refresh must observe:
      // the figures reach the browser over the WebSocket while the turn is still
      // in progress and the DB row is not yet flushed. Round numbers keep the
      // rendered "50K / 200K", "25% used" and "$0.4200" assertions deterministic.
      write({
        jsonrpc: '2.0',
        method: 'session/update',
        params: {
          sessionId: SESSION_ID,
          update: {
            sessionUpdate: 'usage_update',
            size: 200000,
            used: 50000,
            cost: { amount: 0.42, currency: 'USD' },
          },
        },
      });
      // (no result for `id`)
      break;
    default:
      // Unknown request: answer so the client is never left blocking on it.
      if (id !== undefined && id !== null) result(id, {});
      break;
  }
});
