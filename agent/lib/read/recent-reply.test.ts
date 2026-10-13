import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
import { test } from "node:test";
import { buildRecentReplyCliResult, forceRefreshAndRead } from "./recent-reply.ts";
import { resolveMonitorStateFile } from "./monitor-check.ts";

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
