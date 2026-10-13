import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import {
  commandLooksLikeExpectedOwner,
  hasCurrentRunConnection,
  logRunMarkerMatches,
  readLogTail,
  ensureOwner,
} from "./live.ts";

test("new and legacy exact owner argv match, unrelated processes do not", () => {
  const store = path.join(os.tmpdir(), "wacli-owner-test");
  assert.equal(commandLooksLikeExpectedOwner(`/usr/local/bin/wacli --store ${store} sync --follow --events`, store), true);
  assert.equal(commandLooksLikeExpectedOwner(`/usr/local/bin/wacli --store ${store} sync --follow --json`, store), true);
  assert.equal(commandLooksLikeExpectedOwner(`/usr/local/bin/wacli --store ${store} sync --once --events`, store), false);
  assert.equal(commandLooksLikeExpectedOwner(`node --store ${store} sync --follow --events`, store), false);
});

test("bounded log read retains 40 nonempty lines and detects current-run events", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-log-"));
  try {
    const file = path.join(dir, "sync.log");
    const prefix = "old run\nConnected.\n";
    const runId = "test-run";
    const marker = JSON.stringify({ agentOwnerRun: runId }) + "\n";
    const lines = ["", "🙂 first", "", ...Array.from({ length: 45 }, (_, i) => `line-${i}`), '{"event":"connected"}'];
    await fs.writeFile(file, prefix + marker + lines.join("\n") + "\n");
    const offset = Buffer.byteLength(prefix);
    const read = await readLogTail(file, 40, offset);
    assert.equal(read.incomplete, false);
    assert.equal(read.lines.length, 40);
    assert.equal(read.lines.at(-1), '{"event":"connected"}');
    assert.equal(read.lastLifecycleEvent, "connected");
    assert.equal(await logRunMarkerMatches(file, offset, runId), true);
    assert.equal(await logRunMarkerMatches(file, 0, runId), false);
    await fs.appendFile(file, '{"event":"disconnected"}\n');
    assert.equal((await readLogTail(file, 40, offset)).lastLifecycleEvent, "disconnected");
    await fs.truncate(file, 0);
    await fs.writeFile(file, "Connected.\n");
    assert.equal(await logRunMarkerMatches(file, offset, runId), false);
    const rotated = path.join(dir, "rotated.log");
    await fs.rename(file, rotated);
    await fs.writeFile(file, marker + '{"event":"connected"}\n');
    const rotatedRead = await readLogTail(file, 40, 0);
    assert.notEqual(rotatedRead.identity?.ino, read.identity?.ino);
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
});

test("over-limit and partial UTF-8 evidence is visibly incomplete", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-long-log-"));
  try {
    const file = path.join(dir, "sync.log");
    await fs.writeFile(file, "🙂" + "x".repeat(1024 * 1024 + 20) + "\n" + '{"event":"connected"}\n');
    const read = await readLogTail(file, 40);
    assert.equal(read.incomplete, true);
    assert.equal(read.lines.at(-1), '{"event":"connected"}');
    assert.equal(read.lines.some((line) => line.includes("�")), false);
    assert.equal(hasCurrentRunConnection({
      ownerRunning: true, ownerCommandMatches: true, lockHeldByOwner: true,
      logIdentityMatches: true, logEvidenceIncomplete: read.incomplete,
      lastLifecycleEvent: read.lastLifecycleEvent,
    }), false);
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
});

test("large current run needs only recent complete event and forty diagnostics", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-large-log-"));
  try {
    const file = path.join(dir, "sync.log");
    const runId = "recent-run";
    const marker = JSON.stringify({ agentOwnerRun: runId }) + "\n";
    await fs.writeFile(file, marker + "old diagnostic\n".repeat(150_000) +
      '{"event":"connected"}\n' + Array.from({ length: 45 }, (_, i) => `recent-${i}\n`).join(""));
    const read = await readLogTail(file, 40);
    assert.equal(read.incomplete, false);
    assert.equal(read.lastLifecycleEvent, "connected");
    assert.equal(read.lines.length, 40);
    assert.ok(read.scannedBytes < 1024 * 1024);
    assert.equal(await logRunMarkerMatches(file, 0, runId), true);
    assert.equal(hasCurrentRunConnection({
      ownerRunning: true, ownerCommandMatches: true, lockHeldByOwner: true,
      logIdentityMatches: true, logEvidenceIncomplete: read.incomplete,
      lastLifecycleEvent: read.lastLifecycleEvent,
    }), true);
    await fs.appendFile(file, '{"event":"disconnected"');
    const partial = await readLogTail(file, 40);
    assert.equal(partial.lines.at(-1), '{"event":"disconnected"');
    assert.equal(partial.lastLifecycleEvent, "connected");
    assert.equal(partial.incomplete, true);
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
});

test("UTF-8 split across backward blocks is decoded after assembly", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-utf8-log-"));
  try {
    const file = path.join(dir, "sync.log");
    const marker = '{"agentOwnerRun":"utf8"}\n';
    const body = '\n{"event":"connected"}\n' +
      Array.from({ length: 30 }, (_, i) => `line-${i}\n`).join("");
    const filler = "z".repeat(16_382 - Buffer.byteLength(body) - 1) + "\n";
    assert.equal(Buffer.byteLength(body + filler), 16_382);
    await fs.writeFile(file, marker + "🙂" + body + filler);
    const read = await readLogTail(file, 40);
    assert.equal(read.incomplete, false);
    assert.equal(read.lastLifecycleEvent, "connected");
    assert.equal(read.lines.includes("🙂"), true);
    assert.equal(read.lines.some((line) => line.includes("�")), false);
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
});

test("read-only mode rejects owner ensure before touching the store", async () => {
  const previous = process.env.WACLI_READONLY;
  process.env.WACLI_READONLY = "yes";
  try {
    await assert.rejects(
      ensureOwner({
        command: "ensure", json: true, storeDir: path.join(os.tmpdir(), "not-created-owner-test"),
        settleMs: 1, graceMs: 1, timeoutMs: 1, tailLines: 40,
      }),
      /read-only mode/,
    );
  } finally {
    if (previous === undefined) delete process.env.WACLI_READONLY;
    else process.env.WACLI_READONLY = previous;
  }
});

test("ownership, run identity, and connection are independent health evidence", () => {
  const base = {
    ownerRunning: true, ownerCommandMatches: true, lockHeldByOwner: true,
    logIdentityMatches: true, logEvidenceIncomplete: false,
    lastLifecycleEvent: "connected" as const,
  };
  assert.equal(hasCurrentRunConnection(base), true);
  assert.equal(hasCurrentRunConnection({ ...base, logIdentityMatches: false }), false);
  assert.equal(hasCurrentRunConnection({ ...base, lockHeldByOwner: false }), false);
  assert.equal(hasCurrentRunConnection({ ...base, lastLifecycleEvent: "disconnected" }), false);
});
