import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { createHash } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { safeSend } from "./send.ts";
import type { CommandResult } from "./runtime.ts";

const chat = "15551234567@s.whatsapp.net";
const accepted = (extra = {}): CommandResult => ({ ok: true, exitCode: 0, timedOut: false, stderr: "", stdout: JSON.stringify({ success: true, data: { sent: true, to: chat, id: "TEST-SENT", ...extra } }) });
async function fixture() {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "wacli-send-test-"));
  const db = new DatabaseSync(path.join(dir, "wacli.db"));
  db.exec("CREATE TABLE messages(chat_jid TEXT,msg_id TEXT,from_me INTEGER,text TEXT,media_type TEXT,media_caption TEXT,file_sha256 BLOB,quoted_msg_id TEXT)");
  return { dir, db, async close() { db.close(); await fs.rm(dir, { recursive: true, force: true }); } };
}

test("exact multiline text and quote pass through; inspection does not mark read", async () => {
  const f = await fixture();
  try {
    const text = "line one\n`$(not-a-shell-command)` $literal";
    const calls: string[][] = [];
    f.db.prepare("INSERT INTO messages VALUES(?,?,1,?,NULL,NULL,NULL,?)").run(chat, "TEST-SENT", text, "ORIGINAL");
    const result = await safeSend("text", ["--store", f.dir, "--to", chat, "--message", text, "--reply-to", "ORIGINAL"], async (args) => { calls.push(args); return accepted(); });
    assert.equal(calls.length, 1);
    assert.equal(calls[0][calls[0].indexOf("--message") + 1], text);
    assert.equal(calls[0][calls[0].indexOf("--reply-to") + 1], "ORIGINAL");
    assert.equal(result.verification.status, "verified_local");
    assert.equal(result.readState.status, "not_attempted");
    assert.equal(result.quote.status, "verified_local_metadata");
    assert.equal(result.quote.visibleRenderingVerified, false);
  } finally { await f.close(); }
});

test("file receipt is structured and verifies attachment hash, not just caption", async () => {
  const f = await fixture();
  try {
    const file = path.join(f.dir, "test.bin");
    const content = Buffer.from("harmless attachment fixture");
    await fs.writeFile(file, content);
    f.db.prepare("INSERT INTO messages VALUES(?,?,1,?,'document',?,?,NULL)").run(chat, "TEST-SENT", "caption", "caption", createHash("sha256").update(content).digest());
    const result = await safeSend("file", ["--store", f.dir, "--to", chat, "--file", file, "--caption", "caption"], async (args) => { assert.ok(args.includes("--json")); return accepted(); });
    assert.equal(result.verification.status, "verified_local");
    f.db.prepare("UPDATE messages SET file_sha256=?").run(Buffer.from("wrong"));
    const mismatch = await safeSend("file", ["--store", f.dir, "--to", chat, "--file", file, "--caption", "caption", "--settle-ms", "0"], async () => accepted());
    assert.equal(mismatch.verification.status, "unverified");
  } finally { await f.close(); }
});

test("accepted send plus 409 read-state failure never resends", async () => {
  const f = await fixture();
  try {
    f.db.prepare("INSERT INTO messages VALUES(?,?,1,'hello',NULL,NULL,NULL,NULL)").run(chat, "TEST-SENT");
    const calls: string[][] = [];
    const result = await safeSend("text", ["--store", f.dir, "--to", chat, "--message", "hello", "--read-policy", "handled", "--handled-through", "HANDLED"], async (args) => {
      calls.push(args);
      return args.includes("mark-read") ? { ok: false, exitCode: 1, timedOut: false, stderr: "", stdout: JSON.stringify({ success: false, error: "409 conflict" }) } : accepted();
    });
    assert.equal(calls.filter((args) => args.includes("send")).length, 1);
    assert.ok(calls[1].includes("--through-message-id"));
    assert.equal(result.ok, true);
    assert.equal(result.verification.status, "verified_local");
    assert.equal(result.readState.status, "failed");
    assert.equal(result.retryAttempted, false);
  } finally { await f.close(); }
});

test("captionless attachment verifies NULL history caption with the exact file hash", async () => {
  const f = await fixture();
  try {
    const file = path.join(f.dir, "no-caption.bin");
    const content = Buffer.from("captionless fixture");
    await fs.writeFile(file, content);
    f.db.prepare("INSERT INTO messages VALUES(?,?,1,NULL,'document',NULL,?,NULL)").run(chat, "TEST-SENT", createHash("sha256").update(content).digest());
    const result = await safeSend("file", ["--store", f.dir, "--to", chat, "--file", file], async () => accepted());
    assert.equal(result.verification.status, "verified_local");
    assert.equal(result.retryAttempted, false);
  } finally { await f.close(); }
});

test("store warning and malformed/post-dispatch receipts do not trigger read or resend", async () => {
  const f = await fixture();
  try {
    for (const raw of [accepted({ store_warning: "disk full" }), { ok: false, exitCode: null, stdout: "", stderr: "operation may still have gone through", timedOut: true }, { ok: true, exitCode: 0, stdout: "Sent file to a contact", stderr: "", timedOut: false }]) {
      let calls = 0;
      const result = await safeSend("text", ["--store", f.dir, "--to", chat, "--message", "hello", "--read-policy", "handled", "--handled-through", "OLD", "--settle-ms", "0"], async () => { calls++; return raw; });
      assert.equal(calls, 1);
      assert.equal(result.verification.status, "unverified");
      assert.equal(result.readState.status, "not_attempted");
    }
  } finally { await f.close(); }
});

test("missing handled boundary is rejected before dispatch", async () => {
  let calls = 0;
  await assert.rejects(safeSend("text", ["--to", chat, "--message", "hello", "--read-policy", "handled"], async () => { calls++; return accepted(); }), /handled-through/);
  assert.equal(calls, 0);
});

test("mapped LID receipts verify the canonical exact target without another send", async () => {
  const f = await fixture();
  const session = new DatabaseSync(path.join(f.dir, "session.db"));
  try {
    session.exec("CREATE TABLE whatsmeow_lid_map(lid TEXT PRIMARY KEY,pn TEXT NOT NULL)");
    session.prepare("INSERT INTO whatsmeow_lid_map VALUES(?,?)").run("20001", "15551234567");
    f.db.prepare("INSERT INTO messages VALUES(?,?,1,'hello',NULL,NULL,NULL,NULL)").run(chat, "TEST-SENT");
    let sends = 0;
    const result = await safeSend("text", ["--store", f.dir, "--to", "+1 (555) 123-4567", "--message", "hello"], async () => { sends++; return accepted({ to: "20001@lid" }); });
    assert.equal(sends, 1);
    assert.equal(result.verification.status, "verified_local");
    assert.equal(result.verification.chatJid, chat);
    assert.equal(result.readState.status, "not_attempted");
  } finally { session.close(); await f.close(); }
});

test("an exact ID under another recipient cannot satisfy send verification", async () => {
  const f = await fixture();
  try {
    const foreign = "15557654321@s.whatsapp.net";
    f.db.prepare("INSERT INTO messages VALUES(?,?,1,'hello',NULL,NULL,NULL,NULL)").run(foreign, "TEST-SENT");
    let calls = 0;
    const result = await safeSend("text", ["--store", f.dir, "--to", chat, "--message", "hello", "--read-policy", "handled", "--handled-through", "OLD", "--settle-ms", "0"], async () => { calls++; return accepted({ to: foreign }); });
    assert.equal(calls, 1);
    assert.equal(result.verification.status, "unverified");
    assert.match(result.verification.reason, /recipient/);
    assert.equal(result.readState.status, "not_attempted");
  } finally { await f.close(); }
});
