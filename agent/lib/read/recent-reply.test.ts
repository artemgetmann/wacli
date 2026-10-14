import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
import { test } from "node:test";
import { execFile } from "node:child_process";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { buildRecentReplyCliResult, forceRefreshAndRead } from "./recent-reply.ts";
import { resolveMonitorStateFile } from "./monitor-check.ts";

const execFileAsync = promisify(execFile);

test("real refresh entry point locks, catches up and cleans up without a live account", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-refresh-entry-"));
  const dbPath = path.join(dir, "wacli.db");
  const binary = path.join(dir, "fixture-wacli.mjs");
  const lock = path.join(dir, ".openclaw-send-safe.lock");
  const db = new DatabaseSync(dbPath);
  try {
    db.exec(`
      CREATE TABLE chats(jid TEXT, kind TEXT, name TEXT, last_message_ts INTEGER, unread_count INTEGER);
      CREATE TABLE contacts(jid TEXT, phone TEXT, push_name TEXT, full_name TEXT, first_name TEXT, business_name TEXT);
      CREATE TABLE contact_aliases(jid TEXT, alias TEXT);
      CREATE TABLE messages(chat_jid TEXT, msg_id TEXT, sender_jid TEXT, ts INTEGER, from_me INTEGER,
        text TEXT, media_type TEXT, media_caption TEXT, display_text TEXT, chat_name TEXT, sender_name TEXT);
      INSERT INTO chats VALUES ('111@s.whatsapp.net','dm','Test',1,2);
      INSERT INTO contacts VALUES ('111@s.whatsapp.net','111','Test','Test',NULL,NULL);
    `);
    // Exercise the production filesystem and sibling owner-status helper. Only
    // the network-facing sync executable is a fixture, confined to this store.
    // It checks that the real lock exists before making new history readable.
    await fs.writeFile(binary, `#!/usr/bin/env node
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
const [flag, store, ...args] = process.argv.slice(2);
assert.equal(flag, "--store");
assert.deepEqual(args, ["sync", "--once", "--idle-exit", "5s", "--json"]);
assert.equal(store, process.env.WACLI_STORE_DIR);
const owner = JSON.parse(fs.readFileSync(path.join(store, ".openclaw-send-safe.lock", "owner.json"), "utf8"));
assert.equal(owner.operation, "forced-refresh");
process.kill(owner.pid, 0);
if (process.env.REFRESH_FIXTURE_FAIL === "1") {
  process.stderr.write("fixture catch-up failed\\n");
  process.exit(9);
}
const db = new DatabaseSync(path.join(store, "wacli.db"));
db.prepare("INSERT INTO messages VALUES (?,?,?,?,0,?,NULL,NULL,?,'Test','Test')").run("111@s.whatsapp.net", "after-catchup", "111@s.whatsapp.net", 20, "fresh fixture", "fresh fixture");
db.close();
process.stdout.write(JSON.stringify({ success: true }) + "\\n");
`, { mode: 0o700 });
    const env = { ...process.env, WACLI_AGENT_BINARY: binary, WACLI_AGENT_STORE: dir,
      WACLI_STORE_DIR: dir, WACLI_READONLY: "0", REFRESH_FIXTURE_FAIL: "0" };
    const args = ["--experimental-strip-types", fileURLToPath(new URL("./recent-reply.ts", import.meta.url)),
      "--target", "111", "--db", dbPath, "--refresh", "--json"];
    const { stdout } = await execFileAsync(process.execPath, args, { env, timeout: 10_000 });
    const result = JSON.parse(stdout);
    assert.equal(result.latestInboundReply.msgId, "after-catchup");
    assert.equal(result.refresh.freshnessProven, true);
    assert.equal(result.refresh.ownerWasRunning, false);
    assert.equal(result.refresh.ownerRestored, false);
    for (const stage of ["lock", "status", "catchup", "read", "total"]) {
      assert.equal(typeof result.refresh.timingsMs[stage], "number");
    }
    await assert.rejects(fs.stat(lock), { code: "ENOENT" });
    assert.equal(db.prepare("SELECT unread_count FROM chats").get()?.unread_count, 2);
    // A failed catch-up must not emit a success snapshot or strand the lock.
    await assert.rejects(execFileAsync(process.execPath, args, {
      env: { ...env, REFRESH_FIXTURE_FAIL: "1" }, timeout: 10_000,
    }), (error: any) => {
      assert.equal(error.code, 1);
      assert.equal(error.stdout, "");
      const failure = JSON.parse(error.stderr.trim());
      assert.match(failure.error, /fixture catch-up failed/);
      assert.equal(typeof failure.timingsMs.catchup, "number");
      return true;
    });
    await assert.rejects(fs.stat(lock), { code: "ENOENT" });
  } finally {
    db.close();
    await fs.rm(dir, { recursive: true, force: true });
  }
});

test("read-only lookup resolves only a DB-proven sibling and preserves unread state", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-read-"));
  const dbPath = path.join(dir, "wacli.db");
  const db = new DatabaseSync(dbPath);
  try {
    db.exec(`
      CREATE TABLE chats(jid TEXT, kind TEXT, name TEXT, last_message_ts INTEGER, unread_count INTEGER);
      CREATE TABLE contacts(jid TEXT, phone TEXT, push_name TEXT, full_name TEXT, first_name TEXT, business_name TEXT);
      CREATE TABLE contact_aliases(jid TEXT, alias TEXT);
      CREATE TABLE messages(chat_jid TEXT, msg_id TEXT, sender_jid TEXT, ts INTEGER, from_me INTEGER,
        text TEXT, media_type TEXT, media_caption TEXT, display_text TEXT, chat_name TEXT, sender_name TEXT);
      INSERT INTO chats VALUES ('111@s.whatsapp.net','dm','Test',1,2),('opaque@lid','dm','Test',2,1),('unrelated@lid','dm','Test',3,5);
      INSERT INTO contacts VALUES ('111@s.whatsapp.net','111','Test','Test',NULL,NULL),
        ('opaque@lid','111','Test','Test',NULL,NULL),('unrelated@lid',NULL,'Test','Test',NULL,NULL);
      INSERT INTO messages VALUES ('opaque@lid','in-1','opaque@lid',20,0,'reply',NULL,NULL,'reply','Test','Test');
    `);
    const before = db.prepare("SELECT jid, unread_count FROM chats ORDER BY jid").all();
    const result = await buildRecentReplyCliResult({
      dbPath, json: true, lastProcessedMsgId: null, stateFile: null, target: "111",
    });
    assert.equal(result.latestInboundReply?.msgId, "in-1");
    assert.equal(result.preferredMonitorChatJid, "opaque@lid");
    assert.equal(result.candidates.some((candidate) => candidate.jid === "unrelated@lid"), false);
    assert.deepEqual(db.prepare("SELECT jid, unread_count FROM chats ORDER BY jid").all(), before);
    assert.equal(resolveMonitorStateFile("111@s.whatsapp.net", path.join(dir, "state")),
      path.join(dir, "state", "111_s_whatsapp_net.json"));
    const previous = process.env.WACLI_READONLY;
    process.env.WACLI_READONLY = "1";
    try {
      const stateFile = path.join(dir, "state", "target.json");
      const checked = await buildRecentReplyCliResult({
        dbPath, json: true, lastProcessedMsgId: null, stateFile, target: "111",
      });
      assert.equal(checked.monitorStatus, "new_message");
      assert.equal(typeof checked.monitorAckToken, "string");
      await assert.rejects(fs.stat(stateFile), { code: "ENOENT" });
      const stillReadable = await buildRecentReplyCliResult({
        dbPath, json: true, lastProcessedMsgId: null, stateFile: null, target: "111",
      });
      assert.equal(stillReadable.latestInboundReply?.msgId, "in-1");
    } finally {
      if (previous === undefined) delete process.env.WACLI_READONLY;
      else process.env.WACLI_READONLY = previous;
    }
  } finally {
    db.close();
    await fs.rm(dir, { recursive: true, force: true });
  }
});

test("refresh requires current-run restoration and reports every completed stage timing", async () => {
  const args = {
    dbPath: path.join(os.tmpdir(), "wacli-test-store", "wacli.db"),
    json: true, lastProcessedMsgId: null, stateFile: null, target: "111",
  };
  let released = false;
  const deps = {
    acquireStoreLock: async () => ({ release: async () => { released = true; } }),
    statusOwner: async () => ({ ownerRunning: true, ownerCommandMatches: true, ownerPid: 123 }),
    stopOwner: async () => ({ stoppedPid: 123, stopReason: "stopped", ownerRunning: false,
      lockHeldByOwner: false, lockPid: undefined }),
    runBoundedSync: async () => {},
    readResult: async () => ({ target: "111" }) as never,
    ensureOwner: async () => ({ ownerRunning: true, ownerCommandMatches: true,
      lockHeldByOwner: true, connected: false }),
  };
  await assert.rejects(forceRefreshAndRead(args, deps), (error: unknown) => {
    assert.match((error as Error).message, /could not be proven restored/);
    const timings = (error as Error & { timingsMs: Record<string, number> }).timingsMs;
    for (const stage of ["lock", "status", "stop", "catchup", "read", "restore", "total"]) {
      assert.equal(typeof timings[stage], "number");
    }
    return true;
  });
  assert.equal(released, true);
});
