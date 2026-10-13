import fs from "node:fs/promises";
import path from "node:path";
import { randomUUID, createHash } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { defaultStore, envelope, exactText, parseFlags, runWacli, sleep } from "./runtime.ts";
import type { Runner } from "./runtime.ts";

// Keep the existing per-store operation lock during migration: old authored
// refreshes and new sends must not dispatch concurrently against one account.
async function operationLock(store: string, waitMs: number) {
  const dir = path.join(store, ".openclaw-send-safe.lock");
  const nonce = randomUUID();
  const deadline = performance.now() + waitMs;
  await fs.mkdir(store, { recursive: true, mode: 0o700 });
  while (true) {
    try {
      await fs.mkdir(dir, { mode: 0o700 });
      await fs.writeFile(path.join(dir, "owner.json"), JSON.stringify({ pid: process.pid, nonce, acquiredAt: new Date().toISOString() }), { mode: 0o600 });
      return async () => {
        const owner = JSON.parse(await fs.readFile(path.join(dir, "owner.json"), "utf8"));
        if (owner.nonce === nonce) { await fs.unlink(path.join(dir, "owner.json")); await fs.rmdir(dir); }
      };
    } catch (error: any) {
      if (error.code !== "EEXIST") throw error;
      // A missing metadata file can be another caller creating its lock. Never
      // steal it. Clear only a fully recorded owner whose PID is definitely dead.
      try {
        const owner = JSON.parse(await fs.readFile(path.join(dir, "owner.json"), "utf8"));
        if (Number.isInteger(owner.pid) && owner.pid > 0) {
          try { process.kill(owner.pid, 0); }
          catch (probe: any) {
            if (probe.code === "ESRCH") {
              const same = JSON.parse(await fs.readFile(path.join(dir, "owner.json"), "utf8"));
              if (JSON.stringify(same) === JSON.stringify(owner)) { await fs.unlink(path.join(dir, "owner.json")); await fs.rmdir(dir); continue; }
            }
          }
        }
      } catch { /* Incomplete or changed ownership remains conservative. */ }
      if (performance.now() >= deadline) throw new Error("Timed out waiting for the per-store operation lock; nothing was dispatched");
      await sleep(100);
    }
  }
}

function milliseconds(value: unknown, fallback: number) {
  if (value === undefined) return fallback;
  const n = Number(value);
  if (!Number.isSafeInteger(n) || n < 0 || n > 600_000) throw new Error("Invalid millisecond waiting limit");
  return n;
}

// Inspect the exact accepted ID in read-only SQLite. File caption matching is
// insufficient: require the uploaded plaintext hash and an actual media row.
export function verifyHistory(store: string, chat: string, id: string, kind: string, text: string, fileHash?: Buffer) {
  const db = new DatabaseSync(path.join(store, "wacli.db"), { readOnly: true });
  try {
    const row: any = db.prepare("SELECT * FROM messages WHERE chat_jid=? AND msg_id=?").get(chat, id);
    if (!row || !row.from_me) return { status: "unverified", reason: "Exact outbound ID is absent from local history" };
    if (kind === "text" && row.text !== text) return { status: "unverified", reason: "Exact outbound ID has different text" };
    if (kind === "file" && (!row.media_type || row.media_caption !== text || !row.file_sha256 || !Buffer.from(row.file_sha256).equals(fileHash!))) {
      return { status: "unverified", reason: "Attachment hash/media/caption do not match the accepted file" };
    }
    return { status: "verified_local", chatJid: chat, messageId: id, quotedMessageId: row.quoted_msg_id || undefined };
  } finally { db.close(); }
}

export async function safeSend(kind: "text" | "file", argv: string[], runner: Runner = runWacli) {
  if (["1", "true", "yes", "on"].includes((process.env.WACLI_READONLY || "").trim().toLowerCase())) throw new Error("Read-only mode rejects outbound sending");
  const flags = parseFlags(argv);
  const allowed = new Set(["to", "message", "message-file", "file", "caption", "caption-file", "store", "json", "timeout-ms", "lock-wait-ms", "settle-ms", "grace-ms", "reply-to", "reply-to-sender", "read-policy", "handled-through", "allow-self", "no-preview"]);
  for (const key of Object.keys(flags)) if (!allowed.has(key)) throw new Error(`Unsupported --${key}`);
  const store = path.resolve(String(flags.store || defaultStore()));
  const to = String(flags.to || "");
  if (!/^\+?[\d ()-]+$/.test(to) && !/^\d+(?:-\d+)?@(s\.whatsapp\.net|lid|g\.us)$/.test(to)) throw new Error("Safe sends require an exact phone number or chat JID");
  const text = await exactText(kind === "text" ? flags.message : flags.caption, kind === "text" ? flags["message-file"] : flags["caption-file"], kind === "text");
  if (kind === "file" && (flags["allow-self"] || flags["no-preview"])) throw new Error("--allow-self and --no-preview apply to text sends only");
  const policy = flags["read-policy"] || "preserve";
  if (!["preserve", "handled"].includes(String(policy))) throw new Error("--read-policy must be preserve or handled");
  if (policy === "handled" && !flags["handled-through"]) throw new Error("Handled read state requires --handled-through");
  if (flags["handled-through"] && policy !== "handled") throw new Error("--handled-through requires --read-policy handled");
  if (flags["reply-to-sender"] && !flags["reply-to"]) throw new Error("--reply-to-sender requires --reply-to");
  let fileHash: Buffer | undefined;
  if (kind === "file") {
    if (typeof flags.file !== "string") throw new Error("--file is required");
    const stat = await fs.stat(flags.file);
    if (!stat.isFile() || stat.size > 100 * 1024 * 1024) throw new Error("Attachment must be a regular file up to 100 MiB");
    fileHash = createHash("sha256").update(await fs.readFile(flags.file)).digest();
  }
  const timeout = milliseconds(flags["timeout-ms"], 90_000);
  const settle = milliseconds(flags["settle-ms"], 15_000);
  const start = performance.now();
  const release = await operationLock(store, milliseconds(flags["lock-wait-ms"], 180_000));
  const timings: Record<string, number> = { lockWaitMs: performance.now() - start };
  try {
    const args = ["--store", store, "--timeout", `${timeout}ms`, "send", kind, "--to", to, "--json"];
    if (kind === "text") args.push("--message", text); else args.push("--file", String(flags.file), "--caption", text);
    for (const key of ["reply-to", "reply-to-sender"]) if (flags[key]) args.push(`--${key}`, String(flags[key]));
    for (const key of ["allow-self", "no-preview"]) if (flags[key]) args.push(`--${key}`);
    const dispatchedAt = performance.now();
    // Exactly one CLI dispatch. In particular, errors after dispatch cannot
    // enter a lock-recovery retry or a send-again path.
    const raw = await runner(args, timeout + 5_000);
    timings.sendMs = performance.now() - dispatchedAt;
    const receipt = envelope(raw.stdout);
    const data = receipt?.success === true ? receipt.data : undefined;
    const accepted = data?.sent === true;
    const chat = typeof data?.to === "string" ? data.to : undefined;
    const id = typeof data?.id === "string" ? data.id : undefined;
    let verification: any = { status: "unverified", reason: "No structured exact send receipt; dispatch may be uncertain" };
    let readState: any = { status: "not_attempted", policy, reason: "Conversation remains pending" };
    const verifyStart = performance.now();
    if (accepted && chat && id) {
      const deadline = performance.now() + settle;
      do {
        try { verification = verifyHistory(store, chat, id, kind, text, fileHash); }
        catch (error: any) { verification = { status: "unverified", reason: error.message }; }
        if (verification.status === "verified_local" || data.store_warning || performance.now() >= deadline) break;
        await sleep(100);
      } while (true);
    }
    timings.verifyMs = performance.now() - verifyStart;
    if (policy === "handled" && accepted && verification.status === "verified_local") {
      const readStart = performance.now();
      const result = await runner(["--store", store, "--timeout", `${timeout}ms`, "chats", "mark-read", "--chat", chat!, "--through-message-id", String(flags["handled-through"]), "--json"], timeout + 5_000);
      readState = { status: result.ok && envelope(result.stdout)?.success === true ? "marked" : "failed", policy, chatJid: chat, throughMessageId: flags["handled-through"], exitCode: result.exitCode, timedOut: result.timedOut,
        reason: result.ok ? undefined : envelope(result.stdout)?.error || result.stderr.trim() || "Read-state update failed" };
      timings.readStateMs = performance.now() - readStart;
    }
    const quote = { requestedMessageId: flags["reply-to"], status: !flags["reply-to"] ? "not_requested" : verification.quotedMessageId === flags["reply-to"] ? "verified_local_metadata" : "unverified", visibleRenderingVerified: false };
    timings.totalMs = performance.now() - start;
    return { ok: accepted, status: accepted ? "sent" : "failed", command: kind, to, ownerPaused: false, ownerRestored: false,
      send: { exitCode: raw.exitCode, stdout: raw.stdout, stderr: raw.stderr, timedOut: raw.timedOut, accepted, storeWarning: data?.store_warning },
      verification, readState, quote, retryAttempted: false, timings,
      message: accepted ? "WhatsApp accepted the send. Local verification, quote and read state are separate results; do not resend to repair them." : "Send not confirmed. A timeout or malformed receipt may follow dispatch; reconcile before retrying." };
  } finally { await release(); }
}
