import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { test } from "node:test";
import { buildMonitorAckCliResult, parseArgs } from "./monitor-ack.ts";
import { buildMonitorCheckCliResult } from "./monitor-check.ts";
import { decodeMonitorAckSnapshot, encodeMonitorAckSnapshot } from "./monitor.ts";

const chat = "111@s.whatsapp.net";

async function fixture() {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-monitor-"));
  const dbPath = path.join(dir, "wacli.db");
  const stateFile = path.join(dir, "state", "target.json");
  const db = new DatabaseSync(dbPath);
  db.exec(`
    CREATE TABLE chats(jid TEXT, kind TEXT, name TEXT, last_message_ts INTEGER, unread_count INTEGER);
    CREATE TABLE contacts(jid TEXT, phone TEXT, push_name TEXT, full_name TEXT, first_name TEXT, business_name TEXT);
    CREATE TABLE contact_aliases(jid TEXT, alias TEXT);
    CREATE TABLE messages(chat_jid TEXT, msg_id TEXT, sender_jid TEXT, ts INTEGER, from_me INTEGER,
      text TEXT, media_type TEXT, media_caption TEXT, display_text TEXT, chat_name TEXT, sender_name TEXT);
    INSERT INTO chats VALUES ('111@s.whatsapp.net','dm','Target',1,2),('222@s.whatsapp.net','dm','Other',1,3);
    INSERT INTO contacts VALUES ('111@s.whatsapp.net','111','Target','Target',NULL,NULL),
      ('222@s.whatsapp.net','222','Other','Other',NULL,NULL);
  `);
  const add = (id: string, ts = 20, fromMe = 0, jid = chat, text: string | null = "reply") =>
    db.prepare("INSERT INTO messages VALUES (?,?,?,?,?,?,NULL,NULL,?,NULL,NULL)").run(jid, id, jid, ts, fromMe, text, text);
  add("in-1");
  const check = () => buildMonitorCheckCliResult({
    dbPath, stateFile, target: chat, json: true, stateDir: null, lastProcessedMsgId: null,
  });
  const ackArgs = (checked: Awaited<ReturnType<typeof check>>) => ({
    dbPath, stateFile, target: checked.target, chat: checked.latestInboundReply!.chatJid,
    id: checked.latestInboundReply!.msgId, ackToken: checked.monitorAckToken!, json: true,
  });
  return {
    dir, dbPath, stateFile, db, add, check, ackArgs,
    cleanup: async () => { db.close(); await fs.rm(dir, { recursive: true, force: true }); },
  };
}

test("checks preserve failed/unhandled work until explicit exact acknowledgement", async () => {
  const f = await fixture();
  try {
    const first = await f.check();
    // A failed caller performs no acknowledgement. Every retry still reports
    // the same pending message and leaves even the state directory absent.
    const retry = await f.check();
    assert.equal(first.monitorStatus, "new_message");
    assert.equal(retry.monitorStatus, "new_message");
    assert.equal(first.monitorAckToken, retry.monitorAckToken);
    await assert.rejects(fs.stat(path.dirname(f.stateFile)), { code: "ENOENT" });
    assert.equal((await buildMonitorAckCliResult(f.ackArgs(first))).status, "acknowledged");
    assert.equal((await buildMonitorAckCliResult(f.ackArgs(first))).status, "already_acknowledged");
    const acknowledged = await f.check();
    assert.equal(acknowledged.monitorStatus, "no_change");
    assert.equal(acknowledged.monitorBootstrapDecision.reason, "already-processed");
    assert.equal(acknowledged.monitorAckToken, null);
    assert.equal((await fs.stat(f.stateFile)).mode & 0o777, 0o600);
    assert.deepEqual(f.db.prepare("SELECT unread_count FROM chats ORDER BY jid").all().map((row) => row.unread_count), [2, 3]);
  } finally { await f.cleanup(); }
});

test("acknowledging a captured ID preserves later arrivals, including the same second", async () => {
  const f = await fixture();
  try {
    const first = await f.check();
    f.add("in-2", 20);
    await buildMonitorAckCliResult(f.ackArgs(first));
    const second = await f.check();
    assert.equal(second.monitorStatus, "new_message");
    assert.equal(second.latestInboundReply?.msgId, "in-2");
    assert.equal(JSON.parse(await fs.readFile(f.stateFile, "utf8")).lastProcessedMsgId, "in-1");
    await buildMonitorAckCliResult(f.ackArgs(second));
    assert.equal((await f.check()).monitorStatus, "no_change");
    f.add("in-3", 21);
    assert.equal((await f.check()).latestInboundReply?.msgId, "in-3");
  } finally { await f.cleanup(); }
});

test("legacy msgId and lastProcessedMsgId baselines remain intact during check", async () => {
  const f = await fixture();
  try {
    await fs.mkdir(path.dirname(f.stateFile));
    for (const baseline of [{ msgId: "in-1", ts: 20 }, { lastProcessedMsgId: "in-1", msgId: "older", ts: 20 }]) {
      const raw = JSON.stringify(baseline);
      await fs.writeFile(f.stateFile, raw);
      const checked = await f.check();
      assert.equal(checked.monitorStatus, "no_change");
      assert.equal(await fs.readFile(f.stateFile, "utf8"), raw);
    }
    f.add("in-2", 21);
    const next = await f.check();
    assert.equal(next.monitorStatus, "new_message");
    await buildMonitorAckCliResult(f.ackArgs(next));
    const saved = JSON.parse(await fs.readFile(f.stateFile, "utf8"));
    assert.equal(saved.msgId, "in-2");
    assert.equal(saved.lastProcessedMsgId, "in-2");
    assert.equal(saved.target, chat);
  } finally { await f.cleanup(); }
});

test("acknowledgement rejects wrong target/chat/ID/database/state and invalid inbound identity", async () => {
  const f = await fixture();
  try {
    const checked = await f.check();
    const args = f.ackArgs(checked);
    for (const changed of [
      { target: "222@s.whatsapp.net" }, { chat: "222@s.whatsapp.net" }, { id: "missing" },
      { dbPath: path.join(f.dir, "other.db") }, { stateFile: path.join(f.dir, "other.json") },
      { ackToken: "invalid" },
    ]) {
      await assert.rejects(buildMonitorAckCliResult({ ...args, ...changed }), /does not match|Invalid monitor/);
    }
    // The token is not a credential. Even an internally supplied malformed
    // receipt cannot acknowledge outbound, unrenderable or unrelated rows.
    f.add("out", 21, 1);
    f.add("empty", 21, 0, chat, null);
    f.add("other", 21, 0, "222@s.whatsapp.net");
    for (const [id, jid] of [["out", chat], ["empty", chat], ["other", "222@s.whatsapp.net"], ["absent", chat]]) {
      const alteredToken = encodeMonitorAckSnapshot({ ...decodeMonitorAckSnapshot(args.ackToken), msgId: id, chatJid: jid });
      await assert.rejects(buildMonitorAckCliResult({ ...args, id, chat: jid, ackToken: alteredToken }), /exact resolved actionable inbound/);
    }
    await assert.rejects(fs.stat(f.stateFile), { code: "ENOENT" });
  } finally { await f.cleanup(); }
});

test("late and concurrent acknowledgements cannot replace newer completed work", async () => {
  const f = await fixture();
  try {
    const older = await f.check();
    f.add("in-2", 21);
    const newer = await f.check();
    await buildMonitorAckCliResult(f.ackArgs(newer));
    await assert.rejects(buildMonitorAckCliResult(f.ackArgs(older)), /baseline changed/);
    assert.equal(JSON.parse(await fs.readFile(f.stateFile, "utf8")).msgId, "in-2");
    assert.equal((await f.check()).monitorStatus, "no_change");
    f.add("in-3", 22);
    const sameSnapshot = await f.check();
    const results = await Promise.all([
      buildMonitorAckCliResult(f.ackArgs(sameSnapshot)),
      buildMonitorAckCliResult(f.ackArgs(sameSnapshot)),
    ]);
    assert.deepEqual(results.map((result) => result.status).sort(), ["acknowledged", "already_acknowledged"]);
    // A scope-bound baseline also prevents reusing this state path for another
    // contact, even if that other contact happens to have the same message ID.
    await assert.rejects(buildMonitorCheckCliResult({
      dbPath: f.dbPath, stateFile: f.stateFile, target: "222@s.whatsapp.net", json: true,
      stateDir: null, lastProcessedMsgId: null,
    }), /different target or database/);
  } finally { await f.cleanup(); }
});

test("read-only guard runs before acknowledgement creates any state or lock files", async () => {
  const f = await fixture();
  const previous = process.env.WACLI_READONLY;
  try {
    process.env.WACLI_READONLY = "1";
    const checked = await f.check();
    assert.equal(checked.monitorStatus, "new_message");
    await assert.rejects(buildMonitorAckCliResult(f.ackArgs(checked)), /read-only mode/);
    await assert.rejects(fs.stat(path.dirname(f.stateFile)), { code: "ENOENT" });
  } finally {
    if (previous === undefined) delete process.env.WACLI_READONLY;
    else process.env.WACLI_READONLY = previous;
    await f.cleanup();
  }
});

test("corrupt state and ambiguous identity fail closed without baseline replacement", async () => {
  const f = await fixture();
  try {
    const checked = await f.check();
    await fs.mkdir(path.dirname(f.stateFile));
    await fs.writeFile(f.stateFile, "corrupt");
    await assert.rejects(f.check(), /JSON/);
    await assert.rejects(buildMonitorAckCliResult(f.ackArgs(checked)), /JSON/);
    assert.equal(await fs.readFile(f.stateFile, "utf8"), "corrupt");
    await fs.unlink(f.stateFile);
    f.db.exec("DELETE FROM messages; INSERT INTO chats VALUES ('opaque@lid','dm','Target',1,2); INSERT INTO contacts VALUES ('opaque@lid','111','Target','Target',NULL,NULL)");
    await assert.rejects(f.check(), /Unsafe WhatsApp monitor resolution/);
    await assert.rejects(buildMonitorAckCliResult(f.ackArgs(checked)), /exact resolved actionable inbound/);
  } finally { await f.cleanup(); }
});

test("a live acknowledgement lock times out without stealing ownership or pending work", async () => {
  const f = await fixture();
  try {
    const checked = await f.check();
    await fs.mkdir(path.dirname(f.stateFile));
    const lockPath = `${f.stateFile}.ack-lock`;
    const ownership = JSON.stringify({ pid: process.pid, nonce: "live-worker" });
    await fs.writeFile(lockPath, ownership, { mode: 0o600 });
    const start = Date.now();
    await assert.rejects(buildMonitorAckCliResult(f.ackArgs(checked)), /lock is busy/);
    assert.ok(Date.now() - start < 4_000);
    assert.equal(await fs.readFile(lockPath, "utf8"), ownership);
    await assert.rejects(fs.stat(f.stateFile), { code: "ENOENT" });
    assert.equal((await f.check()).monitorStatus, "new_message");
  } finally { await f.cleanup(); }
});

test("a complete lock left by an actually exited owner is reclaimed before acknowledgement", async () => {
  const f = await fixture();
  try {
    const checked = await f.check();
    await fs.mkdir(path.dirname(f.stateFile));
    const lockPath = `${f.stateFile}.ack-lock`;
    // Have a real separate process leave its complete owner record, then wait
    // for its exit. This proves ESRCH recovery rather than guessing a high PID.
    const owner = spawn(process.execPath, ["--input-type=module", "-e",
      'import fs from "node:fs"; fs.writeFileSync(process.argv[1], JSON.stringify({pid:process.pid,nonce:"exited-owner"}), {mode:0o600});',
      lockPath], { stdio: "ignore" });
    const [code] = await once(owner, "exit");
    assert.equal(code, 0);
    assert.throws(() => process.kill(owner.pid!, 0), { code: "ESRCH" });
    assert.equal((await buildMonitorAckCliResult(f.ackArgs(checked))).status, "acknowledged");
    assert.equal((await f.check()).monitorStatus, "no_change");
    await assert.rejects(fs.stat(lockPath), { code: "ENOENT" });
  } finally { await f.cleanup(); }
});

test("an incomplete lock is never reclaimed even when its recorded PID is dead", async () => {
  const f = await fixture();
  try {
    const checked = await f.check();
    await fs.mkdir(path.dirname(f.stateFile));
    const owner = spawn(process.execPath, ["-e", ""], { stdio: "ignore" });
    await once(owner, "exit");
    const raw = JSON.stringify({ pid: owner.pid });
    const lockPath = `${f.stateFile}.ack-lock`;
    await fs.writeFile(lockPath, raw, { mode: 0o600 });
    await assert.rejects(buildMonitorAckCliResult(f.ackArgs(checked)), /lock is busy/);
    assert.equal(await fs.readFile(lockPath, "utf8"), raw);
    assert.equal((await f.check()).monitorStatus, "new_message");
  } finally { await f.cleanup(); }
});

test("ack CLI requires an explicit snapshot and preserves all supported path options", () => {
  assert.throws(() => parseArgs(["--target", chat]), /requires/);
  assert.deepEqual(parseArgs([
    "--target", chat, "--chat", chat, "--id", "in-1", "--ack-token", "receipt",
    "--db", "/tmp/test.db", "--state-dir", "/tmp/monitors", "--json",
  ]), {
    target: chat, chat, id: "in-1", ackToken: "receipt", dbPath: "/tmp/test.db",
    stateFile: null, stateDir: "/tmp/monitors", json: true,
  });
});
