import { createHash, randomUUID } from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { requireWritable } from "../owner/store.ts";
import { defaultDbPath, defaultStateDir, resolveMonitorStateFile } from "./monitor-check.ts";
import { decodeMonitorAckSnapshot, readMonitorState, verifyMonitorScope } from "./monitor.ts";
import { findLatestInboundReplyAcrossResolvedChats } from "./reconciliation.ts";

export type Args = {
  dbPath: string;
  target: string;
  chat: string;
  id: string;
  ackToken: string;
  stateFile: string | null;
  stateDir?: string | null;
  json: boolean;
};

export function parseArgs(argv: string[]): Args {
  const args: Args = {
    dbPath: defaultDbPath(), target: "", chat: "", id: "", ackToken: "",
    stateFile: null, stateDir: null, json: false,
  };
  const options = {
    "--db": "dbPath", "--target": "target", "--chat": "chat", "--id": "id",
    "--ack-token": "ackToken", "--state-file": "stateFile", "--state-dir": "stateDir",
  } as const;
  for (let index = 0; index < argv.length; index += 1) {
    const option = argv[index];
    if (option === "--json") { args.json = true; continue; }
    const key = options[option as keyof typeof options];
    const value = argv[++index];
    if (!key || !value || value.startsWith("--")) {
      throw new Error("Usage: wacli agent monitor-ack --target <target> --chat <jid> --id <id> --ack-token <token> [--db <path>] [--state-file <path>|--state-dir <path>] [--json]");
    }
    args[key] = value;
  }
  if (![args.target, args.chat, args.id, args.ackToken].every((value) => value.trim())) {
    throw new Error("monitor-ack requires --target, --chat, --id and --ack-token from the successful check");
  }
  return args;
}

async function readStateLock(lockPath: string) {
  const before = await fs.lstat(lockPath);
  if (!before.isFile()) throw new Error("Unknown monitor lock ownership");
  const raw = await fs.readFile(lockPath, "utf8");
  const owner = JSON.parse(raw);
  const after = await fs.lstat(lockPath);
  if (before.dev !== after.dev || before.ino !== after.ino ||
      !Number.isSafeInteger(owner.pid) || owner.pid <= 0 ||
      typeof owner.nonce !== "string" || !owner.nonce.trim()) {
    throw new Error("Incomplete or changed monitor lock ownership");
  }
  return { raw, pid: owner.pid as number, nonce: owner.nonce as string, dev: after.dev, ino: after.ino };
}

async function removeUnchangedStateLock(lockPath: string, expected: Awaited<ReturnType<typeof readStateLock>>): Promise<boolean> {
  const current = await readStateLock(lockPath);
  // The nonce and contents identify the acquisition; inode/device identify its
  // actual file. Neither a replaced file nor a rewritten owner may be removed.
  if (current.raw !== expected.raw || current.nonce !== expected.nonce ||
      current.dev !== expected.dev || current.ino !== expected.ino) return false;
  await fs.unlink(lockPath);
  return true;
}

async function acquireStateLock(stateFile: string): Promise<() => Promise<void>> {
  const lockPath = `${stateFile}.ack-lock`;
  const deadline = Date.now() + 2_000;
  const nonce = randomUUID();
  // Recover only fully recorded owners with a definitely dead PID. Live,
  // inaccessible and incomplete ownership stays locked for the bounded wait.
  while (true) {
    // Recovery retries share the same deadline as occupied-lock waits, so
    // repeated abandoned records cannot turn acknowledgement into an unbounded loop.
    if (Date.now() >= deadline) throw new Error("Monitor acknowledgement lock is busy; baseline unchanged");
    try {
      const lock = await fs.open(lockPath, "wx", 0o600);
      try {
        await lock.writeFile(JSON.stringify({ pid: process.pid, nonce }), "utf8");
        await lock.sync();
        const owned = await readStateLock(lockPath);
        return async () => {
          await lock.close();
          await removeUnchangedStateLock(lockPath, owned);
        };
      } catch (error) {
        await lock.close();
        throw error;
      }
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
      try {
        const owner = await readStateLock(lockPath);
        let definitelyDead = false;
        try { process.kill(owner.pid, 0); }
        catch (probe) { definitelyDead = (probe as NodeJS.ErrnoException).code === "ESRCH"; }
        if (definitelyDead && await removeUnchangedStateLock(lockPath, owner)) continue;
      } catch { /* Unknown, incomplete or changed ownership is never stolen. */ }
      if (Date.now() >= deadline) throw new Error("Monitor acknowledgement lock is busy; baseline unchanged");
      await delay(25);
    }
  }
}

export async function buildMonitorAckCliResult(args: Args): Promise<{
  status: "acknowledged" | "already_acknowledged";
  target: string;
  chatJid: string;
  msgId: string;
  stateFile: string;
}> {
  // Run the guard before directory, lock or temporary-file creation. Checks
  // remain usable in read-only mode; acknowledgement is an explicit mutation.
  requireWritable("monitor acknowledgement");
  const target = args.target.trim();
  const stateFile = path.resolve(args.stateFile ?? resolveMonitorStateFile(target, args.stateDir ?? defaultStateDir()));
  const snapshot = decodeMonitorAckSnapshot(args.ackToken);
  if (snapshot.target !== target || snapshot.dbPath !== path.resolve(args.dbPath) ||
      snapshot.stateFile !== stateFile || snapshot.chatJid !== args.chat || snapshot.msgId !== args.id) {
    throw new Error("Monitor acknowledgement does not match the exact checked target, database, state file, chat and message");
  }

  // Resolve identity afresh, but validate the captured message rather than
  // selecting current latest. New arrivals must survive this acknowledgement.
  const lookup = findLatestInboundReplyAcrossResolvedChats({
    dbPath: args.dbPath, target, seedMsgId: args.id,
  });
  const message = lookup.seedMessage;
  if (!lookup.candidates.some((candidate) => candidate.jid === args.chat) ||
      !message || message.chatJid !== args.chat || message.msgId !== args.id ||
      message.fromMe || !message.hasRenderableContent) {
    throw new Error("Monitor acknowledgement requires the exact resolved actionable inbound message");
  }

  await fs.mkdir(path.dirname(stateFile), { recursive: true, mode: 0o700 });
  const release = await acquireStateLock(stateFile);
  try {
    const baseline = await readMonitorState(stateFile);
    verifyMonitorScope(baseline.state, target, args.dbPath);
    const ackTokenHash = createHash("sha256").update(args.ackToken).digest("hex");
    const result = { target, chatJid: args.chat, msgId: args.id, stateFile };
    // A lost command receipt may be retried only for this exact completed
    // snapshot. An older token must never become an unconditional baseline set.
    if (baseline.state?.target === target && baseline.state.dbPath === snapshot.dbPath &&
        baseline.state.chatJid === args.chat && baseline.state.lastProcessedMsgId === args.id &&
        baseline.state.ackTokenHash === ackTokenHash) {
      return { status: "already_acknowledged", ...result };
    }
    if (baseline.hash !== snapshot.previousStateHash) {
      throw new Error("Monitor baseline changed since check; refusing stale acknowledgement");
    }

    const tempFile = `${stateFile}.${randomUUID()}.tmp`;
    try {
      // Rename publishes one complete baseline. Creating the replacement with
      // 0600 also tightens a legacy file's permissions without exposing content.
      const temp = await fs.open(tempFile, "wx", 0o600);
      try {
        await temp.writeFile(`${JSON.stringify({
          lastProcessedMsgId: args.id, msgId: args.id, ts: message.ts,
          target, dbPath: snapshot.dbPath, chatJid: args.chat, ackTokenHash,
        }, null, 2)}\n`, "utf8");
        await temp.sync();
      } finally { await temp.close(); }
      await fs.rename(tempFile, stateFile);
    } finally {
      await fs.unlink(tempFile).catch((error: NodeJS.ErrnoException) => {
        if (error.code !== "ENOENT") throw error;
      });
    }
    return { status: "acknowledged", ...result };
  } finally { await release(); }
}

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2));
  const result = await buildMonitorAckCliResult(args);
  console.log(args.json ? JSON.stringify(result, null, 2) : `${result.status}: ${result.chatJid} ${result.msgId}`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  void main().catch((error) => {
    console.error(JSON.stringify({ error: error instanceof Error ? error.message : String(error) }));
    process.exitCode = 1;
  });
}
