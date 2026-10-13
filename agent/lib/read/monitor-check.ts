import path from "node:path";
import { fileURLToPath } from "node:url";
import { buildRecentReplyCliResult } from "./recent-reply.ts";
import { defaultStoreDir } from "../owner/store.ts";

type Args = {
  dbPath: string;
  json: boolean;
  lastProcessedMsgId: string | null;
  stateDir: string | null;
  stateFile: string | null;
  target: string | null;
};

type MonitorCheckResult = Awaited<ReturnType<typeof buildMonitorCheckCliResult>>;

function printUsage(): never {
  console.error(`Usage: wacli agent monitor-check --target <phone|jid> [--db <path>] [--json] [--state-dir <path>] [--state-file <path>] [--last-processed-msg-id <id>]

Examples:
  wacli agent monitor-check --target 111@s.whatsapp.net --json
  wacli agent monitor-check --target +111 --state-dir /tmp/wacli-monitor --json
  wacli agent monitor-check --target +111 --state-file /tmp/wacli-monitor.json --json
`);
  process.exit(1);
}

export function defaultDbPath() {
  return path.join(defaultStoreDir(), "wacli.db");
}

export function defaultStateDir() {
  return path.join(defaultStoreDir(), "agent-monitor-state");
}

export function resolveMonitorStateFile(target: string, stateDir: string) {
  const normalizedTarget = target
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "");
  const fileBase = normalizedTarget.length > 0 ? normalizedTarget : "target";
  return path.join(stateDir, `${fileBase}.json`);
}

function parseArgs(argv: string[]): Args {
  const args: Args = {
    dbPath: defaultDbPath(),
    json: false,
    lastProcessedMsgId: null,
    stateDir: null,
    stateFile: null,
    target: null,
  };

  for (let idx = 0; idx < argv.length; idx += 1) {
    const arg = argv[idx];
    if (arg === "--db") {
      args.dbPath = argv[idx + 1] ?? printUsage();
      idx += 1;
      continue;
    }
    if (arg === "--target") {
      args.target = argv[idx + 1] ?? printUsage();
      idx += 1;
      continue;
    }
    if (arg === "--last-processed-msg-id") {
      args.lastProcessedMsgId = argv[idx + 1] ?? printUsage();
      idx += 1;
      continue;
    }
    if (arg === "--state-dir") {
      args.stateDir = argv[idx + 1] ?? printUsage();
      idx += 1;
      continue;
    }
    if (arg === "--state-file") {
      args.stateFile = argv[idx + 1] ?? printUsage();
      idx += 1;
      continue;
    }
    if (arg === "--json") {
      args.json = true;
      continue;
    }
    printUsage();
  }

  if (!args.target?.trim()) {
    printUsage();
  }

  return args;
}

export async function buildMonitorCheckCliResult(args: Args): Promise<{
  target: string;
  monitorStatus?: "new_message" | "no_change";
  status?: "new_message" | "no_change";
  stateFile: string;
  preferredMonitorChatJid: string;
  latestInboundReply: Awaited<ReturnType<typeof buildRecentReplyCliResult>>["latestInboundReply"];
  recentConversation: Awaited<ReturnType<typeof buildRecentReplyCliResult>>["recentConversation"];
  continuity: Awaited<ReturnType<typeof buildRecentReplyCliResult>>["continuity"];
  monitorBootstrapDecision: Awaited<
    ReturnType<typeof buildRecentReplyCliResult>
  >["monitorBootstrapDecision"];
  candidates: Awaited<ReturnType<typeof buildRecentReplyCliResult>>["candidates"];
  seedJids: string[];
  seedPhones: string[];
  identityNames: string[];
  monitorAckToken: string | null;
}> {
  const target = args.target!.trim();
  const resolvedStateFile =
    args.stateFile ?? resolveMonitorStateFile(target, args.stateDir ?? defaultStateDir());
  const result = await buildRecentReplyCliResult({
    dbPath: args.dbPath,
    json: args.json,
    lastProcessedMsgId: args.lastProcessedMsgId,
    stateFile: resolvedStateFile,
    target,
  });

  return {
    ...result,
    stateFile: resolvedStateFile,
  };
}

function printHumanResult(result: MonitorCheckResult) {
  console.log(`Target: ${result.target}`);
  console.log(`Monitor status: ${result.monitorStatus ?? result.status ?? "no_change"}`);
  console.log(`State file: ${result.stateFile}`);
  console.log(
    `Bootstrap decision: ${result.monitorBootstrapDecision.action} (${result.monitorBootstrapDecision.reason})`,
  );
  console.log(`Preferred monitor chat: ${result.preferredMonitorChatJid}`);
  if (!result.latestInboundReply) {
    console.log("Latest inbound reply: none");
    return;
  }
  console.log("Latest inbound reply:");
  console.log(`- msg_id: ${result.latestInboundReply.msgId}`);
  console.log(`- chat: ${result.latestInboundReply.chatJid}`);
  console.log(`- ts: ${result.latestInboundReply.ts}`);
  console.log(`- effective text: ${result.latestInboundReply.effectiveText ?? "(empty)"}`);
  console.log(`- recent turns: ${result.recentConversation.length}`);
  if (result.continuity.lastOutboundReply) {
    console.log(
      `- last outbound: ${result.continuity.lastOutboundReply.effectiveText ?? "(empty)"}`,
    );
  }
  if (result.continuity.lastOutboundIsRepeatOfPrevious) {
    console.log("- repeat risk: last outbound duplicates the previous outbound");
  }
}

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2));
  const result = await buildMonitorCheckCliResult(args);
  if (args.json) {
    console.log(JSON.stringify(result, null, 2));
    return;
  }
  printHumanResult(result);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  void main();
}

export { parseArgs };
export type { Args };
