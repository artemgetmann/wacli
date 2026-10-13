import { createHash } from "node:crypto";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { envelope, runWacli } from "../runtime.ts";
import type { CommandResult, Runner } from "../runtime.ts";
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

export async function buildMonitorAckCliResult(args: Args, stateWriter: Runner = runWacli): Promise<{
  status: "acknowledged" | "already_acknowledged";
  target: string;
  chatJid: string;
  msgId: string;
  stateFile: string;
}> {
  // Run the guard before dispatching native publication. Checks remain usable
  // in read-only mode; acknowledgement is an explicit mutation.
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

  const baseline = await readMonitorState(stateFile);
  verifyMonitorScope(baseline.state, target, args.dbPath);
  const ackTokenHash = createHash("sha256").update(args.ackToken).digest("hex");
  // Native publication owns one uninterrupted kernel-lock/CAS/atomic-write
  // operation. Node must never reclaim or unlink that lock: pathname checks
  // cannot prevent a competing recoverer from removing a new live owner's file.
  let command: CommandResult;
  try {
    command = await stateWriter([
      "agent-monitor-state-ack", "--state-file", stateFile,
      "--expected-hash", snapshot.previousStateHash, "--ack-token-hash", ackTokenHash,
      "--target", target, "--db", snapshot.dbPath, "--chat", args.chat,
      "--id", args.id, "--ts", String(message.ts), "--json",
    ], 5_000);
  } catch {
    throw new Error("Monitor acknowledgement outcome is uncertain; recheck the baseline or retry the exact token");
  }
  const receipt = envelope(command.stdout);
  if (!command.ok || command.timedOut || receipt?.success !== true ||
      !["acknowledged", "already_acknowledged"].includes(receipt?.data?.status)) {
    const detail = typeof receipt?.error === "string" ? receipt.error : command.stderr.trim();
    throw new Error(`Monitor acknowledgement outcome is uncertain; recheck the baseline or retry the exact token${detail ? `: ${detail}` : ""}`);
  }
  return { status: receipt.data.status, target, chatJid: args.chat, msgId: args.id, stateFile };
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
