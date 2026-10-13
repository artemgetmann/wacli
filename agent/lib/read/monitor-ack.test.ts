import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
import { createHash } from "node:crypto";
import { test } from "node:test";
import { buildMonitorAckCliResult, parseArgs } from "./monitor-ack.ts";
import { buildMonitorCheckCliResult } from "./monitor-check.ts";
import { decodeMonitorAckSnapshot, encodeMonitorAckSnapshot } from "./monitor.ts";
import type { Args } from "./monitor-ack.ts";
import type { Runner } from "../runtime.ts";

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
  let publication = Promise.resolve();
  const requests: string[][] = [];
  // Simulate native receipts for caller-level semantic tests. Native Go tests
  // separately prove kernel exclusion, crash release and atomic publication.
  const writer: Runner = (argv) => {
    requests.push(argv);
    const operation = publication.then(async () => {
      const flags = new Map<string, string>();
      for (let index = 1; index < argv.length - 1; index += 2) flags.set(argv[index], argv[index + 1]);
      let raw: string | null;
      try { raw = await fs.readFile(stateFile, "utf8"); }
      catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
        raw = null;
      }
      const previous = raw === null ? null : JSON.parse(raw);
      const scope = { target: flags.get("--target"), dbPath: flags.get("--db"), chatJid: flags.get("--chat") };
      const id = flags.get("--id");
      const tokenHash = flags.get("--ack-token-hash");
      let status = "acknowledged";
      if (previous?.target === scope.target && previous.dbPath === scope.dbPath &&
          previous.chatJid === scope.chatJid && previous.lastProcessedMsgId === id &&
          previous.ackTokenHash === tokenHash) {
        status = "already_acknowledged";
      } else {
        const hash = createHash("sha256").update(raw === null ? "missing" : `present:${raw}`).digest("hex");
        if (hash !== flags.get("--expected-hash")) {
          return { ok: false, exitCode: 1, timedOut: false, stderr: "", stdout: JSON.stringify({ success: false, error: "Monitor baseline changed since check" }) };
        }
        await fs.mkdir(path.dirname(stateFile), { recursive: true });
        await fs.writeFile(stateFile, JSON.stringify({
          ...scope, lastProcessedMsgId: id, msgId: id, ts: Number(flags.get("--ts")), ackTokenHash: tokenHash,
        }), { mode: 0o600 });
      }
      return { ok: true, exitCode: 0, timedOut: false, stderr: "", stdout: JSON.stringify({ success: true, data: { status } }) };
    });
    publication = operation.then(() => undefined, () => undefined);
    return operation;
  };
  const ack = (args: Args, stateWriter: Runner = writer) => buildMonitorAckCliResult(args, stateWriter);
  return {
    dir, dbPath, stateFile, db, add, check, ackArgs, ack, writer, requests,
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
    assert.equal((await f.ack(f.ackArgs(first))).status, "acknowledged");
    assert.equal((await f.ack(f.ackArgs(first))).status, "already_acknowledged");
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
    await f.ack(f.ackArgs(first));
    const second = await f.check();
    assert.equal(second.monitorStatus, "new_message");
    assert.equal(second.latestInboundReply?.msgId, "in-2");
    assert.equal(JSON.parse(await fs.readFile(f.stateFile, "utf8")).lastProcessedMsgId, "in-1");
    await f.ack(f.ackArgs(second));
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
    await f.ack(f.ackArgs(next));
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
      await assert.rejects(f.ack({ ...args, ...changed }), /does not match|Invalid monitor/);
    }
    // The token is not a credential. Even an internally supplied malformed
    // receipt cannot acknowledge outbound, unrenderable or unrelated rows.
    f.add("out", 21, 1);
    f.add("empty", 21, 0, chat, null);
    f.add("other", 21, 0, "222@s.whatsapp.net");
    for (const [id, jid] of [["out", chat], ["empty", chat], ["other", "222@s.whatsapp.net"], ["absent", chat]]) {
      const alteredToken = encodeMonitorAckSnapshot({ ...decodeMonitorAckSnapshot(args.ackToken), msgId: id, chatJid: jid });
      await assert.rejects(f.ack({ ...args, id, chat: jid, ackToken: alteredToken }), /exact resolved actionable inbound/);
    }
    await assert.rejects(fs.stat(f.stateFile), { code: "ENOENT" });
    assert.equal(f.requests.length, 0);
  } finally { await f.cleanup(); }
});

test("late and concurrent acknowledgements cannot replace newer completed work", async () => {
  const f = await fixture();
  try {
    const older = await f.check();
    f.add("in-2", 21);
    const newer = await f.check();
    await f.ack(f.ackArgs(newer));
    await assert.rejects(f.ack(f.ackArgs(older)), /baseline changed/);
    assert.equal(JSON.parse(await fs.readFile(f.stateFile, "utf8")).msgId, "in-2");
    assert.equal((await f.check()).monitorStatus, "no_change");
    f.add("in-3", 22);
    const sameSnapshot = await f.check();
    const results = await Promise.all([
      f.ack(f.ackArgs(sameSnapshot)),
      f.ack(f.ackArgs(sameSnapshot)),
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
    await assert.rejects(f.ack(f.ackArgs(checked)), /read-only mode/);
    assert.equal(f.requests.length, 0);
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
    await assert.rejects(f.ack(f.ackArgs(checked)), /JSON/);
    assert.equal(await fs.readFile(f.stateFile, "utf8"), "corrupt");
    await fs.unlink(f.stateFile);
    f.db.exec("DELETE FROM messages; INSERT INTO chats VALUES ('opaque@lid','dm','Target',1,2); INSERT INTO contacts VALUES ('opaque@lid','111','Target','Target',NULL,NULL)");
    await assert.rejects(f.check(), /Unsafe WhatsApp monitor resolution/);
    await assert.rejects(f.ack(f.ackArgs(checked)), /exact resolved actionable inbound/);
  } finally { await f.cleanup(); }
});

test("ack delegates the exact snapshot to native publication and requires its receipt", async () => {
  const f = await fixture();
  try {
    const checked = await f.check();
    const args = f.ackArgs(checked);
    let calls = 0;
    const writer: Runner = async (argv, timeoutMs) => {
      calls += 1;
      assert.equal(timeoutMs, 5_000);
      assert.deepEqual(argv, [
        "agent-monitor-state-ack", "--state-file", f.stateFile,
        "--expected-hash", decodeMonitorAckSnapshot(args.ackToken).previousStateHash,
        "--ack-token-hash", createHash("sha256").update(args.ackToken).digest("hex"),
        "--target", chat, "--db", f.dbPath, "--chat", chat, "--id", "in-1", "--ts", "20", "--json",
      ]);
      return { ok: true, exitCode: 0, timedOut: false, stderr: "", stdout: JSON.stringify({ success: true, data: { status: "acknowledged" } }) };
    };
    assert.equal((await f.ack(args, writer)).status, "acknowledged");
    assert.equal(calls, 1);
    // Node performs no directory, baseline, temporary-file or lock mutation.
    await assert.rejects(fs.stat(path.dirname(f.stateFile)), { code: "ENOENT" });
  } finally { await f.cleanup(); }
});

test("failed, malformed and timed-out native receipts remain uncertain without automatic retry", async () => {
  const f = await fixture();
  try {
    const checked = await f.check();
    const good = { ok: true, exitCode: 0, timedOut: false, stderr: "", stdout: JSON.stringify({ success: true, data: { status: "acknowledged" } }) };
    for (const result of [
      { ...good, stdout: "not JSON" }, { ...good, stdout: '{"success":true,"data":{"status":"other"}}' },
      { ...good, stdout: '{"success":false,"error":"publication failed"}' },
      { ...good, ok: false, exitCode: 1 }, { ...good, timedOut: true },
    ]) {
      let calls = 0;
      await assert.rejects(f.ack(f.ackArgs(checked), async () => { calls += 1; return result; }), /outcome is uncertain/);
      assert.equal(calls, 1);
    }
    await assert.rejects(f.ack(f.ackArgs(checked), async () => { throw new Error("child unavailable"); }), /outcome is uncertain/);
    await assert.rejects(fs.stat(path.dirname(f.stateFile)), { code: "ENOENT" });
    assert.equal((await f.check()).monitorStatus, "new_message");
  } finally { await f.cleanup(); }
});

test("a lost native receipt is reconciled only by retrying the exact acknowledgement token", async () => {
  const f = await fixture();
  try {
    const checked = await f.check();
    const args = f.ackArgs(checked);
    await assert.rejects(f.ack(args, async (argv, timeoutMs) => {
      await f.writer(argv, timeoutMs);
      return { ok: false, exitCode: null, timedOut: true, stderr: "", stdout: "" };
    }), /outcome is uncertain/);
    assert.equal((await f.check()).monitorStatus, "no_change");
    assert.equal((await f.ack(args)).status, "already_acknowledged");
    assert.equal(f.requests.length, 2);
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
