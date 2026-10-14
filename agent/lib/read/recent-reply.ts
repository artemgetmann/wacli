import { execFile } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { performance } from "node:perf_hooks";
import { defaultStoreDir, requireWritable } from "../owner/store.ts";
import {
  decideWacliMonitorBootstrapAction,
  resolvePreferredMonitorChatJid,
  readMonitorState,
  verifyMonitorScope,
  encodeMonitorAckSnapshot,
} from "./monitor.ts";
import { findLatestInboundReplyAcrossResolvedChats } from "./reconciliation.ts";

type Args = {
  dbPath: string;
  json: boolean;
  lastProcessedMsgId: string | null;
  refresh?: boolean;
  stateFile: string | null;
  target: string | null;
};

type RefreshResult = {
  attempted: boolean;
  freshnessProven: boolean;
  ownerRestored: boolean;
  ownerWasRunning: boolean;
  succeeded: boolean;
  timingsMs: Record<string, number>;
};

type LiveOwnerStatus = {
  connected?: boolean;
  lockHeldByOwner?: boolean;
  lockPid?: number;
  ownerCommandMatches?: boolean;
  ownerPid?: number;
  ownerRunning?: boolean;
  stoppedPid?: number;
  stopReason?: string;
};

type StoreLock = {
  release: () => Promise<void>;
};

const execFileAsync = promisify(execFile);

type RecentReplyCliResult = Awaited<ReturnType<typeof buildRecentReplyCliResult>>;

export type RefreshDeps = {
  acquireStoreLock: (storeDir: string) => Promise<StoreLock>;
  ensureOwner: (storeDir: string) => Promise<LiveOwnerStatus>;
  readResult: (args: Args) => Promise<RecentReplyCliResult>;
  runBoundedSync: (storeDir: string) => Promise<void>;
  statusOwner: (storeDir: string) => Promise<LiveOwnerStatus>;
  stopOwner: (storeDir: string) => Promise<LiveOwnerStatus>;
};

function printUsage(): never {
  console.error(`Usage: wacli agent recent-reply --target <phone|jid> [--db <path>] [--json] [--refresh] [--state-file <path>] [--last-processed-msg-id <id>]

Examples:
  wacli agent recent-reply --target 111@s.whatsapp.net --json
  wacli agent recent-reply --target 111@s.whatsapp.net --refresh --json
  wacli agent recent-reply --target +111 --last-processed-msg-id inbound-17 --json
  wacli agent recent-reply --target +111 --state-file /tmp/wacli-monitor-state.json --json
  wacli agent recent-reply --target +111
`);
  process.exit(1);
}

export function parseArgs(argv: string[]): Args {
  const args: Args = {
    dbPath: path.join(defaultStoreDir(), "wacli.db"),
    json: false,
    lastProcessedMsgId: null,
    refresh: false,
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
    if (arg === "--state-file") {
      args.stateFile = argv[idx + 1] ?? printUsage();
      idx += 1;
      continue;
    }
    if (arg === "--json") {
      args.json = true;
      continue;
    }
    if (arg === "--refresh") {
      args.refresh = true;
      continue;
    }
    printUsage();
  }

  if (!args.target?.trim()) {
    printUsage();
  }

  return args;
}

async function runJsonCommand(
  command: string,
  args: string[],
  timeout: number,
): Promise<Record<string, unknown>> {
  const { stdout } = await execFileAsync(command, args, {
    encoding: "utf8",
    maxBuffer: 4 * 1024 * 1024,
    timeout,
  });
  return JSON.parse(stdout) as Record<string, unknown>;
}

function liveScriptPath(): string {
  const scriptDir = path.dirname(fileURLToPath(import.meta.url));
  return path.join(scriptDir, "../owner/live.ts");
}

async function runLiveCommand(
  action: "ensure" | "status" | "stop",
  storeDir: string,
): Promise<LiveOwnerStatus> {
  const timeoutMs = action === "status" ? 10_000 : action === "stop" ? 15_000 : 25_000;
  return (await runJsonCommand(
    process.execPath,
    ["--experimental-strip-types", liveScriptPath(), action, "--store", storeDir, "--json"],
    timeoutMs,
  )) as LiveOwnerStatus;
}

function isPidRunning(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    // EPERM still proves that a live process owns the PID.
    const code = typeof error === "object" && error && "code" in error ? error.code : undefined;
    return code === "EPERM";
  }
}

async function readStoreLockOwner(lockDir: string): Promise<{ pid?: number } | undefined> {
  try {
    const raw = await fs.readFile(path.join(lockDir, "owner.json"), "utf8");
    return JSON.parse(raw) as { pid?: number };
  } catch {
    return undefined;
  }
}

async function acquireStoreLock(storeDir: string): Promise<StoreLock> {
  await fs.mkdir(storeDir, { recursive: true, mode: 0o700 });

  // Keep the legacy lock path during handoff: send and forced catch-up pause
  // the same owner, and both versions must serialize those operations.
  const lockDir = path.join(storeDir, ".openclaw-send-safe.lock");
  const deadline = Date.now() + 180_000;
  while (Date.now() <= deadline) {
    let createdLockDir = false;
    try {
      await fs.mkdir(lockDir);
      createdLockDir = true;
      await fs.writeFile(
        path.join(lockDir, "owner.json"),
        `${JSON.stringify({
          pid: process.pid,
          acquiredAt: new Date().toISOString(),
          operation: "forced-refresh",
          storeDir,
        })}\n`,
        { encoding: "utf8", mode: 0o600 },
      );
      return {
        release: async () => {
          await fs.rm(lockDir, { recursive: true, force: true });
        },
      };
    } catch (error) {
      // A metadata write failure after mkdir must not strand an ownerless lock
      // that every later safe send or refresh would correctly refuse to clear.
      if (createdLockDir) {
        await fs.rm(lockDir, { recursive: true, force: true });
      }
      const code = typeof error === "object" && error && "code" in error ? error.code : undefined;
      if (code !== "EEXIST") {
        throw error;
      }
    }

    // Recover only a lock whose recorded process is definitely gone. Missing
    // or unreadable identity fails closed instead of deleting an active lock.
    const owner = await readStoreLockOwner(lockDir);
    if (typeof owner?.pid === "number" && !isPidRunning(owner.pid)) {
      await fs.rm(lockDir, { recursive: true, force: true });
      continue;
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }

  const owner = await readStoreLockOwner(lockDir);
  const ownerHint =
    typeof owner?.pid === "number" ? ` Recorded lock owner PID: ${String(owner.pid)}.` : "";
  throw new Error(`Timed out waiting for the wacli store operation lock.${ownerHint}`);
}

function stopProvesExactOwnerPaused(status: LiveOwnerStatus, expectedPid: number): boolean {
  return Boolean(
    status.stoppedPid === expectedPid &&
    (status.stopReason === "stopped" || status.stopReason === "forced_stop") &&
    status.ownerRunning !== true &&
    status.lockHeldByOwner !== true &&
    status.lockPid !== expectedPid,
  );
}

function restorationIsProven(status: LiveOwnerStatus): boolean {
  // "Connected" is a lifecycle hint, not ownership proof. Require the newly
  // ensured process to match the exact owner command and hold this store lock.
  return Boolean(
    status.ownerRunning &&
    status.ownerCommandMatches &&
    status.lockHeldByOwner &&
    status.connected,
  );
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function composeRefreshErrors(primary: unknown, restoration: unknown): Error {
  return new AggregateError(
    [primary, restoration],
    `Forced WhatsApp catch-up failed: ${errorMessage(primary)} Restoration also failed: ${errorMessage(restoration)}`,
  );
}

const defaultRefreshDeps: RefreshDeps = {
  acquireStoreLock,
  ensureOwner: async (storeDir) => await runLiveCommand("ensure", storeDir),
  readResult: async (args) => await buildRecentReplyCliResult(args),
  runBoundedSync: async (storeDir) => {
    await execFileAsync(
      process.env.WACLI_AGENT_BINARY || "wacli",
      ["--store", storeDir, "sync", "--once", "--idle-exit", "5s", "--json"],
      {
        encoding: "utf8",
        maxBuffer: 4 * 1024 * 1024,
        timeout: 45_000,
      },
    );
  },
  statusOwner: async (storeDir) => await runLiveCommand("status", storeDir),
  stopOwner: async (storeDir) => await runLiveCommand("stop", storeDir),
};

export async function forceRefreshAndRead(
  args: Args,
  deps: RefreshDeps = defaultRefreshDeps,
): Promise<{ refresh: RefreshResult; result: RecentReplyCliResult }> {
  requireWritable("forced refresh");
  const timingsMs: Record<string, number> = {};
  const totalStart = performance.now();
  // Record failed stages too: a long catch-up and a failed restoration must be
  // distinguishable from time spent waiting for our operation lock.
  const measure = async <T>(stage: string, operation: () => Promise<T>): Promise<T> => {
    const start = performance.now();
    try {
      return await operation();
    } finally {
      timingsMs[stage] = Math.round((performance.now() - start) * 100) / 100;
    }
  };
  const storeDir = path.dirname(path.resolve(args.dbPath));
  try {
  const lock = await measure("lock", () => deps.acquireStoreLock(storeDir));
  try {
    const ownerBefore = await measure("status", () => deps.statusOwner(storeDir));
    const ownerWasRunning = ownerBefore.ownerRunning === true;
    if (ownerWasRunning && ownerBefore.ownerCommandMatches !== true) {
      throw new Error(
        "Refusing forced WhatsApp catch-up because the recorded live owner command does not match",
      );
    }

    const expectedOwnerPid = ownerWasRunning ? ownerBefore.ownerPid : undefined;
    if (ownerWasRunning && typeof expectedOwnerPid !== "number") {
      throw new Error(
        "Refusing forced WhatsApp catch-up because the running owner PID is unavailable",
      );
    }

    let primaryError: unknown;
    let restorationError: unknown;
    let result: RecentReplyCliResult | undefined;
    let freshnessProven = false;

    try {
      if (expectedOwnerPid !== undefined) {
        const stopped = await measure("stop", () => deps.stopOwner(storeDir));
        if (!stopProvesExactOwnerPaused(stopped, expectedOwnerPid)) {
          throw new Error(
            "Refusing forced WhatsApp catch-up because the recorded owner changed while it was being paused",
          );
        }
      }

      // A clean bounded sync exit is the freshness boundary: only after it
      // finishes do we reread the caller's exact database path.
      await measure("catchup", () => deps.runBoundedSync(storeDir));
      freshnessProven = true;
      result = await measure("read", () => deps.readResult(args));
    } catch (error) {
      primaryError = error;
    } finally {
      if (expectedOwnerPid !== undefined) {
        try {
          const restored = await measure("restore", () => deps.ensureOwner(storeDir));
          if (!restorationIsProven(restored)) {
            throw new Error(
              "The recorded live owner could not be proven restored with its command and store lock",
            );
          }
        } catch (error) {
          restorationError = error;
        }
      }
    }

    if (primaryError && restorationError) {
      throw composeRefreshErrors(primaryError, restorationError);
    }
    if (restorationError) {
      throw restorationError;
    }
    if (primaryError) {
      throw primaryError;
    }
    if (!freshnessProven || !result) {
      throw new Error("Forced WhatsApp catch-up completed without freshness and reread proof");
    }

    return {
      refresh: {
        attempted: true,
        freshnessProven: true,
        ownerRestored: ownerWasRunning,
        ownerWasRunning,
        succeeded: true,
        timingsMs,
      },
      result,
    };
  } finally {
    await lock.release();
  }
  } catch (error) {
    const failure = error instanceof Error ? error : new Error(String(error));
    Object.assign(failure, { timingsMs });
    throw failure;
  } finally {
    timingsMs.total = Math.round((performance.now() - totalStart) * 100) / 100;
  }
}

export async function buildRecentReplyCliResult(args: Args): Promise<{
  target: string;
  seedJids: string[];
  seedPhones: string[];
  identityNames: string[];
  candidates: Awaited<ReturnType<typeof findLatestInboundReplyAcrossResolvedChats>>["candidates"];
  latestInboundReply: Awaited<
    ReturnType<typeof findLatestInboundReplyAcrossResolvedChats>
  >["latestInboundReply"];
  recentConversation: Awaited<
    ReturnType<typeof findLatestInboundReplyAcrossResolvedChats>
  >["recentConversation"];
  continuity: Awaited<ReturnType<typeof findLatestInboundReplyAcrossResolvedChats>>["continuity"];
  preferredMonitorChatJid: string;
  monitorBootstrapDecision: ReturnType<typeof decideWacliMonitorBootstrapAction>;
  monitorStatus?: "new_message" | "no_change";
  status?: "new_message" | "no_change";
  stateFile?: string | null;
  monitorAckToken: string | null;
}> {
  // Capture the previous baseline before looking up messages. If another
  // worker acknowledges during lookup, this token must fail the later CAS
  // rather than pairing an older database snapshot with its newer baseline.
  const baseline = args.stateFile ? await readMonitorState(args.stateFile) : null;
  const persistedState = baseline?.state;
  verifyMonitorScope(persistedState ?? null, args.target!, args.dbPath);
  const result = findLatestInboundReplyAcrossResolvedChats({
    dbPath: args.dbPath,
    target: args.target!,
  });
  const preferredMonitorChatJid = resolvePreferredMonitorChatJid(result);
  const effectiveLastProcessedMsgId =
    persistedState?.lastProcessedMsgId ?? persistedState?.msgId ?? args.lastProcessedMsgId;
  const bootstrapDecision = decideWacliMonitorBootstrapAction({
    lastProcessedMsgId: effectiveLastProcessedMsgId,
    lookup: result,
  });
  const monitorStatus = args.stateFile
    ? bootstrapDecision.action === "process-latest"
      ? "new_message"
      : "no_change"
    : undefined;
  // Check never advances the baseline. Transcription, interpretation and relay
  // happen in the caller; only its later explicit acknowledgement may persist.
  const monitorAckToken =
    args.stateFile &&
    bootstrapDecision.action === "process-latest" &&
    result.latestInboundReply && baseline
      ? encodeMonitorAckSnapshot({
          version: 1, target: result.target, dbPath: path.resolve(args.dbPath),
          stateFile: path.resolve(args.stateFile), previousStateHash: baseline.hash,
          chatJid: result.latestInboundReply.chatJid, msgId: result.latestInboundReply.msgId,
        })
      : null;

  return {
    ...result,
    preferredMonitorChatJid,
    monitorBootstrapDecision: bootstrapDecision,
    monitorStatus,
    status: monitorStatus,
    stateFile: args.stateFile,
    monitorAckToken,
  };
}

function printHumanResult(result: RecentReplyCliResult): void {
  console.log(`Target: ${result.target}`);
  console.log(`Seed JIDs: ${result.seedJids.join(", ") || "(none)"}`);
  console.log(`Identity names: ${result.identityNames.join(", ") || "(none)"}`);
  console.log(`Preferred monitor chat: ${result.preferredMonitorChatJid}`);
  console.log(
    `Bootstrap decision: ${result.monitorBootstrapDecision.action} (${result.monitorBootstrapDecision.reason})`,
  );
  if (result.stateFile) {
    console.log(`Monitor status: ${result.monitorStatus ?? result.status ?? "no_change"}`);
    console.log(`State file: ${result.stateFile}`);
  }
  console.log("Candidates:");
  for (const candidate of result.candidates) {
    const name = candidate.name ? ` (${candidate.name})` : "";
    console.log(`- ${candidate.jid}${name} [${candidate.reasons.join(", ")}]`);
  }
  if (!result.latestInboundReply) {
    console.log("Latest inbound reply: none");
    return;
  }
  console.log("Latest inbound reply:");
  console.log(`- chat: ${result.latestInboundReply.chatJid}`);
  console.log(`- sender: ${result.latestInboundReply.senderJid ?? "(unknown)"}`);
  console.log(`- ts: ${result.latestInboundReply.ts}`);
  console.log(`- media: ${result.latestInboundReply.mediaType ?? "(none)"}`);
  console.log(`- effective text: ${result.latestInboundReply.effectiveText ?? "(empty)"}`);
  console.log(`Recent conversation turns: ${result.recentConversation.length}`);
  if (result.continuity.lastOutboundReply) {
    console.log(
      `Last outbound reply: ${result.continuity.lastOutboundReply.effectiveText ?? "(empty)"}`,
    );
  }
  if (result.continuity.lastOutboundIsRepeatOfPrevious) {
    console.log("Repeat risk: last outbound duplicates the previous outbound");
  }
}

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2));
  const refreshed = args.refresh ? await forceRefreshAndRead(args) : undefined;
  const result = refreshed?.result ?? (await buildRecentReplyCliResult(args));
  const refresh = refreshed?.refresh;

  if (args.json) {
    console.log(
      JSON.stringify(
        {
          ...result,
          ...(refresh ? { refresh } : {}),
          ...(args.stateFile ? { monitorStatus: result.monitorStatus, status: result.status } : {}),
        },
        null,
        2,
      ),
    );
    return;
  }

  printHumanResult(result);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  void main().catch((error) => {
    const timingsMs = error && typeof error === "object" && "timingsMs" in error
      ? error.timingsMs : undefined;
    console.error(JSON.stringify({ error: errorMessage(error), timingsMs }));
    process.exitCode = 1;
  });
}
