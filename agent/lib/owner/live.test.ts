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
