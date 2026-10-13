import { spawn } from "node:child_process";
import fs from "node:fs";
import fsp from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { randomUUID } from "node:crypto";

type Command = "status" | "ensure" | "stop";

export type Flags = {
  command: Command;
  json: boolean;
  storeDir: string;
  settleMs: number;
  graceMs: number;
  timeoutMs: number;
  tailLines: number;
};

type OwnerFiles = {
  pidPath: string;
  logPath: string;
};

type OwnerPidFile = {
  pid: number;
  startedAt: string;
  storeDir: string;
  command: string[];
  host: string;
  runId?: string;
  logDev?: number;
  logIno?: number;
  logStartOffset?: number;
};

type LifecycleEvent = "connected" | "disconnected" | "reconnecting" | "stopping" | "unknown";

export type LiveStatus = {
  ok: boolean;
  action: Command;
  storeDir: string;
  pidFile: string;
  logFile: string;
  ownerRunning: boolean;
  ownerPid?: number;
  ownerStartedAt?: string;
  ownerAgeMs?: number;
  ownerCommandMatches: boolean;
  lockInfo?: string;
  lockPid?: number;
  lockPidRunning?: boolean;
  lockHeldByOwner: boolean;
  lastLifecycleEvent?: LifecycleEvent;
  connected: boolean;
  logTail: string[];
  logEvidenceIncomplete?: boolean;
  runId?: string;
  message: string;
  stoppedPid?: number;
  stopSignal?: "SIGINT" | "SIGKILL";
  forcedStop?: boolean;
  pidFileRemoved?: boolean;
  stopReason?: "no_pid_file" | "not_running" | "stopped" | "forced_stop" | "pid_command_mismatch";
};

export type EnsureOwnerDeps = {
  collectStatus: (flags: Flags) => Promise<LiveStatus>;
  now: () => number;
  sleep: (ms: number) => Promise<void>;
  startOwner: (flags: Flags, files: OwnerFiles) => Promise<void>;
  stopOwner: (flags: Flags) => Promise<LiveStatus>;
};

const UNHEALTHY_LIFECYCLE_EVENTS = new Set<LifecycleEvent>(["disconnected", "reconnecting"]);
const MIN_RESTART_AGE_MS = 30_000;
const MAX_RECONNECT_GRACE_MS = 5_000;

function usage() {
  console.error(`Usage:
  wacli agent live status [--json] [--store <dir>] [--tail-lines <n>]
  wacli agent live ensure [--json] [--store <dir>] [--settle-ms <ms>] [--tail-lines <n>]
  wacli agent live stop [--json] [--store <dir>] [--grace-ms <ms>] [--tail-lines <n>]

Notes:
  - Owns one long-lived 'wacli sync --follow --events' process per store.
  - Uses a PID file plus the wacli LOCK file and sync log to infer live state.
  - This exists because 'wacli doctor' reports connected=false while another
    sync owner holds the store lock, which is not a live connection probe.`);
}

function defaultStoreDir() {
  return path.join(os.homedir(), ".wacli");
}

function parsePositiveInt(raw: string, flag: string) {
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    throw new Error(`Invalid ${flag}: ${raw}`);
  }
  return parsed;
}

function parseArgs(argv: string[]): Flags {
  const [commandRaw, ...rest] = argv;
  if (!commandRaw || commandRaw === "-h" || commandRaw === "--help") {
    usage();
    process.exit(0);
  }
  if (!["status", "ensure", "stop"].includes(commandRaw)) {
    throw new Error(`Unknown command: ${commandRaw}`);
  }

  const flags: Flags = {
    command: commandRaw as Command,
    json: false,
    storeDir: defaultStoreDir(),
    settleMs: 15_000,
    graceMs: 5_000,
    timeoutMs: 2_000,
    tailLines: 40,
  };

  for (let index = 0; index < rest.length; index += 1) {
    const arg = rest[index];
    switch (arg) {
      case "--json":
        flags.json = true;
        break;
      case "--store": {
        const next = rest[index + 1];
        if (!next) {
          throw new Error("--store requires a value");
        }
        flags.storeDir = path.resolve(next);
        index += 1;
        break;
      }
      case "--settle-ms": {
        const next = rest[index + 1];
        if (!next) {
          throw new Error("--settle-ms requires a value");
        }
        flags.settleMs = parsePositiveInt(next, "--settle-ms");
        index += 1;
        break;
      }
      case "--grace-ms": {
        const next = rest[index + 1];
        if (!next) {
          throw new Error("--grace-ms requires a value");
        }
        flags.graceMs = parsePositiveInt(next, "--grace-ms");
        index += 1;
        break;
      }
      case "--timeout-ms": {
        const next = rest[index + 1];
        if (!next) {
          throw new Error("--timeout-ms requires a value");
        }
        flags.timeoutMs = parsePositiveInt(next, "--timeout-ms");
        index += 1;
        break;
      }
      case "--tail-lines": {
        const next = rest[index + 1];
        if (!next) {
          throw new Error("--tail-lines requires a value");
        }
        flags.tailLines = parsePositiveInt(next, "--tail-lines");
        index += 1;
        break;
      }
      default:
        throw new Error(`Unexpected argument: ${arg}`);
    }
  }

  return flags;
}

function resolveOwnerFiles(storeDir: string): OwnerFiles {
  // Keep legacy filenames during handoff so the old and new helpers cannot
  // each claim a separate owner for the same store.
  return {
    pidPath: path.join(storeDir, "openclaw-sync-owner.json"),
    logPath: path.join(storeDir, "openclaw-sync.log"),
  };
}

async function readPidFile(pidPath: string): Promise<OwnerPidFile | undefined> {
  try {
    const raw = await fsp.readFile(pidPath, "utf8");
    return JSON.parse(raw) as OwnerPidFile;
  } catch {
    return undefined;
  }
}

async function writePidFile(pidPath: string, payload: OwnerPidFile) {
  await fsp.writeFile(pidPath, `${JSON.stringify(payload, null, 2)}\n`, "utf8");
}

async function readLockInfo(storeDir: string): Promise<{ raw?: string; pid?: number }> {
  try {
    const raw = (await fsp.readFile(path.join(storeDir, "LOCK"), "utf8")).trim();
    const pidMatch = raw.match(/pid=(\d+)/);
    return {
      raw: raw || undefined,
      pid: pidMatch ? Number.parseInt(pidMatch[1] ?? "", 10) : undefined,
    };
  } catch {
    return {};
  }
}

function processExists(pid: number) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

async function runShortCommand(
  command: string,
  args: string[],
  timeoutMs: number,
): Promise<{ exitCode: number | null; stdout: string; stderr: string }> {
  return await new Promise((resolve) => {
    const child = spawn(command, args, {
      stdio: ["ignore", "pipe", "pipe"],
      env: process.env,
    });

    let stdout = "";
    let stderr = "";
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
    }, timeoutMs);

    child.stdout.on("data", (chunk) => {
      stdout += String(chunk);
    });
    child.stderr.on("data", (chunk) => {
      stderr += String(chunk);
    });
    child.on("close", (exitCode) => {
      clearTimeout(timer);
      resolve({ exitCode, stdout, stderr });
    });
  });
}

export function commandLooksLikeExpectedOwner(command: string, storeDir: string): boolean {
  // ps returns argv as one display string. Normalize separator whitespace, then
  // require both the wacli executable and exact owner arguments so an unrelated
  // interpreter that merely receives wacli-looking argv is never signal-safe.
  const normalizedCommand = command.trim().replace(/\s+/gu, " ");
  const normalizedStoreDir = storeDir.trim().replace(/\s+/gu, " ");
  const firstSeparator = normalizedCommand.indexOf(" ");
  const executable =
    firstSeparator === -1 ? normalizedCommand : normalizedCommand.slice(0, firstSeparator);
  const args = firstSeparator === -1 ? "" : normalizedCommand.slice(firstSeparator + 1);
  if (path.basename(executable) !== "wacli") {
    return false;
  }
  return (
    args === `--store ${normalizedStoreDir} sync --follow --events` ||
    args === `--store ${normalizedStoreDir} sync --follow --json` ||
    // Older owners omitted --store and therefore always used wacli's default.
    // Never extend that legacy trust to a custom store: doing so could signal a
    // default-store owner recorded in the wrong custom-store PID file.
    (path.resolve(storeDir) === path.resolve(defaultStoreDir()) &&
      (args === "sync --follow --json" || args === "sync --follow --events"))
  );
}

async function commandMatchesExpected(pid: number, storeDir: string) {
  const result = await runShortCommand("ps", ["-o", "command=", "-p", String(pid)], 1_500);
  return commandLooksLikeExpectedOwner(result.stdout, storeDir);
}

export async function readLogTail(
  logPath: string,
  tailLines: number,
  runStartOffset = 0,
): Promise<{ lines: string[]; incomplete: boolean; identity?: { dev: number; ino: number }; lastLifecycleEvent?: LifecycleEvent }> {
  let handle: fsp.FileHandle | undefined;
  try {
    handle = await fsp.open(logPath, "r");
    const stat = await handle.stat();
    const end = stat.size;
    // Read backwards in bounded chunks. A partial leading line is dropped so
    // UTF-8 boundaries and a very long line cannot masquerade as an event.
    let cursor = end;
    let bytes = 0;
    const blocks: Buffer[] = [];
    while (cursor > runStartOffset && bytes < 1024 * 1024) {
      const count = Math.min(16 * 1024, cursor - runStartOffset, 1024 * 1024 - bytes);
      if (count <= 0) break;
      cursor -= count;
      const block = Buffer.allocUnsafe(count);
      const { bytesRead } = await handle.read(block, 0, count, cursor);
      if (bytesRead !== count) throw new Error("Short owner log read");
      blocks.unshift(block);
      bytes += count;
    }
    const incomplete = cursor > runStartOffset;
    const text = Buffer.concat(blocks).toString("utf8");
    const raw = text.split(/\r?\n/u);
    if (incomplete) raw.shift();
    if (end > runStartOffset && !text.endsWith("\n")) raw.pop();
    const completeLines = raw.map((line) => line.trimEnd()).filter(Boolean);
    return {
      lines: completeLines.slice(-tailLines),
      incomplete,
      identity: { dev: stat.dev, ino: stat.ino },
      lastLifecycleEvent: inferLifecycleEvent(completeLines),
    };
  } catch {
    return { lines: [], incomplete: true };
  } finally {
    await handle?.close();
  }
}

export async function logRunMarkerMatches(
  logPath: string,
  offset: number | undefined,
  runId: string | undefined,
): Promise<boolean> {
  if (!runId || !Number.isSafeInteger(offset) || offset! < 0) return false;
  try {
    const handle = await fsp.open(logPath, "r");
    try {
      const marker = Buffer.alloc(256);
      const { bytesRead } = await handle.read(marker, 0, marker.length, offset);
      const line = marker.subarray(0, bytesRead).toString("utf8").split("\n", 1)[0];
      return (JSON.parse(line) as { agentOwnerRun?: string }).agentOwnerRun === runId;
    } finally {
      await handle.close();
    }
  } catch {
    return false;
  }
}

function inferLifecycleEvent(lines: string[]): LifecycleEvent | undefined {
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    const line = lines[index] ?? "";
    // Upstream --events writes one structured JSON record per line.
    try {
      const parsed = JSON.parse(line) as { event?: string };
      if (["connected", "disconnected", "reconnecting", "stopping"].includes(parsed.event ?? "")) {
        return parsed.event as LifecycleEvent;
      }
    } catch {
      // Legacy owners wrote text; retain it only for diagnostics.
    }
    if (line.includes("Connected.")) {
      return "connected";
    }
    if (line.includes("Disconnected.")) {
      return "disconnected";
    }
    if (line.includes("Reconnecting...")) {
      return "reconnecting";
    }
    if (line.includes("Stopping sync.")) {
      return "stopping";
    }
  }
  return undefined;
}

export function hasCurrentRunConnection(evidence: {
  ownerRunning: boolean;
  ownerCommandMatches: boolean;
  lockHeldByOwner: boolean;
  logIdentityMatches: boolean;
  logEvidenceIncomplete: boolean;
  lastLifecycleEvent?: LifecycleEvent;
}): boolean {
  return Boolean(
    evidence.ownerRunning &&
    evidence.ownerCommandMatches &&
    evidence.lockHeldByOwner &&
    evidence.logIdentityMatches &&
    !evidence.logEvidenceIncomplete &&
    evidence.lastLifecycleEvent === "connected",
  );
}

async function collectStatus(flags: Flags): Promise<LiveStatus> {
  const files = resolveOwnerFiles(flags.storeDir);
  const pidFile = await readPidFile(files.pidPath);
  const ownerPid = pidFile?.pid;
  const ownerStartedAt = pidFile?.startedAt;
  const parsedStartedAt = ownerStartedAt ? Date.parse(ownerStartedAt) : Number.NaN;
  const ownerAgeMs = Number.isFinite(parsedStartedAt)
    ? Math.max(0, Date.now() - parsedStartedAt)
    : undefined;
  const ownerRunning = typeof ownerPid === "number" && ownerPid > 0 && processExists(ownerPid);
  const ownerCommandMatches =
    ownerRunning && ownerPid ? await commandMatchesExpected(ownerPid, flags.storeDir) : false;
  const lockInfo = await readLockInfo(flags.storeDir);
  const lockPidRunning =
    typeof lockInfo.pid === "number" && lockInfo.pid > 0 ? processExists(lockInfo.pid) : undefined;
  const log = await readLogTail(files.logPath, flags.tailLines, pidFile?.logStartOffset ?? 0);
  const logIdentityMatches = Boolean(
    pidFile?.runId &&
      Number.isSafeInteger(pidFile.logStartOffset) &&
      pidFile.logStartOffset! >= 0 &&
      pidFile.logDev === log.identity?.dev &&
      pidFile.logIno === log.identity?.ino &&
      await logRunMarkerMatches(files.logPath, pidFile.logStartOffset, pidFile.runId),
  );
  const lastLifecycleEvent = logIdentityMatches ? log.lastLifecycleEvent : undefined;
  const lockHeldByOwner =
    ownerRunning &&
    ownerCommandMatches &&
    typeof ownerPid === "number" &&
    typeof lockInfo.pid === "number" &&
    ownerPid === lockInfo.pid;
  const connected = hasCurrentRunConnection({
    ownerRunning,
    ownerCommandMatches,
    lockHeldByOwner,
    logIdentityMatches,
    logEvidenceIncomplete: log.incomplete,
    lastLifecycleEvent,
  });

  let message = "No wacli sync owner is running.";
  if (connected) {
    message = "Wacli sync owner is running and reported a current-run connection.";
  } else if (ownerRunning && ownerCommandMatches) {
    message = "Wacli sync owner is running, but current-run connection is not confirmed.";
  } else if (ownerRunning) {
    message =
      "A process with the recorded PID is alive, but it no longer looks like wacli sync --follow.";
  }

  return {
    ok: true,
    action: flags.command,
    storeDir: flags.storeDir,
    pidFile: files.pidPath,
    logFile: files.logPath,
    ownerRunning,
    ownerPid,
    ownerStartedAt,
    ownerAgeMs,
    ownerCommandMatches,
    lockInfo: lockInfo.raw,
    lockPid: lockInfo.pid,
    lockPidRunning,
    lockHeldByOwner,
    lastLifecycleEvent,
    connected,
    logTail: log.lines,
    logEvidenceIncomplete: log.incomplete,
    runId: pidFile?.runId,
    message,
  };
}

async function startOwner(flags: Flags, files: OwnerFiles): Promise<void> {
  const logHandle = await fsp.open(files.logPath, "a", 0o600);
  try {
    const stat = await logHandle.stat();
    const runId = randomUUID();
    // The marker survives ordinary append and rejects same-inode truncation
    // that would otherwise let a later run inherit this run's byte offset.
    await logHandle.write(`${JSON.stringify({ agentOwnerRun: runId })}\n`);
    const command = ["wacli", "--store", flags.storeDir, "sync", "--follow", "--events"];
    const child = spawn(process.env.WACLI_AGENT_BINARY || "wacli", command.slice(1), {
      detached: true,
      stdio: ["ignore", logHandle.fd, logHandle.fd],
      env: process.env,
    });
    child.unref();

    await writePidFile(files.pidPath, {
      pid: child.pid ?? -1,
      startedAt: new Date().toISOString(),
      storeDir: flags.storeDir,
      command,
      host: os.hostname(),
      runId,
      logDev: stat.dev,
      logIno: stat.ino,
      logStartOffset: stat.size,
    });
  } finally {
    await logHandle.close();
  }
}

function isExplicitlyUnhealthyOwner(status: LiveStatus): boolean {
  return Boolean(
    status.ownerRunning &&
    status.ownerCommandMatches &&
    status.lastLifecycleEvent &&
    UNHEALTHY_LIFECYCLE_EVENTS.has(status.lastLifecycleEvent),
  );
}

const defaultEnsureOwnerDeps: EnsureOwnerDeps = {
  collectStatus,
  now: Date.now,
  sleep: async (ms) => await new Promise((resolve) => setTimeout(resolve, ms)),
  startOwner,
  stopOwner: async (flags) => await stopOwner(flags),
};

export async function ensureOwner(
  flags: Flags,
  deps: EnsureOwnerDeps = defaultEnsureOwnerDeps,
): Promise<LiveStatus> {
  await fsp.mkdir(flags.storeDir, { recursive: true, mode: 0o700 });
  const files = resolveOwnerFiles(flags.storeDir);

  let initial = await deps.collectStatus(flags);
  if (initial.connected) {
    return initial;
  }

  // A live PID with unexpected argv is an ownership conflict. Never start a
  // second writer while that identity is unresolved.
  if (initial.ownerRunning && !initial.ownerCommandMatches) {
    initial.ok = false;
    return initial;
  }
  if (!initial.ownerRunning && initial.lockPidRunning) {
    initial.ok = false;
    initial.message = "A live process holds the store lock, but its owner identity is unknown.";
    return initial;
  }

  if (initial.ownerRunning && initial.ownerCommandMatches) {
    if (!isExplicitlyUnhealthyOwner(initial)) {
      return initial;
    }

    // A newly spawned owner commonly reports reconnecting before its first
    // Connected event. Keep that startup window quiet so repeated health probes
    // cannot churn a process that is still doing normal session setup.
    if (typeof initial.ownerAgeMs === "number" && initial.ownerAgeMs < MIN_RESTART_AGE_MS) {
      initial.message =
        "Wacli sync owner is reconnecting within its startup grace period.";
      return initial;
    }

    // An established owner gets one short chance to self-recover. If its latest
    // lifecycle event remains explicitly unhealthy, restart it once; do not
    // loop restarts inside a single ensure request.
    const reconnectDeadline = deps.now() + Math.min(flags.settleMs, MAX_RECONNECT_GRACE_MS);
    while (deps.now() < reconnectDeadline) {
      await deps.sleep(Math.min(500, Math.max(1, reconnectDeadline - deps.now())));
      initial = await deps.collectStatus(flags);
      if (initial.connected) {
        initial.message =
          "Wacli sync owner recovered its live connection without a restart.";
        return initial;
      }
      if (!isExplicitlyUnhealthyOwner(initial)) {
        return initial;
      }
    }

    // stopOwner re-checks the live process command immediately before signaling
    // it. That second check is essential: the PID can exit and be reused after
    // collectStatus, and a replacement process must never inherit our signal.
    await deps.stopOwner(flags);
  }

  await deps.startOwner(flags, files);

  const deadline = deps.now() + flags.settleMs;
  for (;;) {
    const status = await deps.collectStatus(flags);
    if (status.connected) {
      status.message = "Wacli started a sync owner and it reported a current-run connection.";
      return status;
    }
    if (!status.ownerRunning && deps.now() > deadline) {
      status.message = "Wacli failed to keep a sync owner running.";
      return status;
    }
    if (deps.now() > deadline) {
      status.message =
        "Wacli started a sync owner, but current-run connection did not settle in time.";
      return status;
    }
    await deps.sleep(500);
  }
}

async function stopOwner(flags: Flags): Promise<LiveStatus> {
  const files = resolveOwnerFiles(flags.storeDir);
  const pidFile = await readPidFile(files.pidPath);
  const ownerPid = pidFile?.pid;
  let stoppedPid: number | undefined;
  let stopSignal: LiveStatus["stopSignal"];
  let forcedStop = false;
  let stopReason: LiveStatus["stopReason"] = "no_pid_file";

  if (ownerPid && processExists(ownerPid)) {
    const matchesExpected = await commandMatchesExpected(ownerPid, flags.storeDir);
    if (!matchesExpected) {
      await fsp.rm(files.pidPath, { force: true });
      const status = await collectStatus(flags);
      status.pidFileRemoved = true;
      status.stopReason = "pid_command_mismatch";
      status.message =
        "Cleared a stale wacli owner PID file without signaling the non-matching process.";
      return status;
    }

    stoppedPid = ownerPid;
    stopSignal = "SIGINT";
    process.kill(ownerPid, "SIGINT");
    const deadline = Date.now() + flags.graceMs;
    while (processExists(ownerPid) && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
    if (processExists(ownerPid)) {
      // PID reuse after SIGINT must not turn the grace timeout into a signal
      // against an unrelated process.
      if (!(await commandMatchesExpected(ownerPid, flags.storeDir))) {
        const status = await collectStatus(flags);
        status.ok = false;
        status.stopReason = "pid_command_mismatch";
        status.message = "Recorded owner PID changed before the forced stop.";
        return status;
      }
      process.kill(ownerPid, "SIGKILL");
      stopSignal = "SIGKILL";
      forcedStop = true;
    }
    stopReason = forcedStop ? "forced_stop" : "stopped";
  } else if (ownerPid) {
    stopReason = "not_running";
  }

  // The owner lock can lag behind the process exit. Give it a short window to
  // disappear, then remove only stale locks that are definitely not live.
  const lockDeadline = Date.now() + Math.min(flags.graceMs, 1_000);
  for (;;) {
    const lockInfo = await readLockInfo(flags.storeDir);
    if (typeof lockInfo.pid !== "number") {
      break;
    }
    const lockPidRunning = processExists(lockInfo.pid);
    if (!lockPidRunning) {
      await fsp.rm(path.join(flags.storeDir, "LOCK"), { force: true });
      break;
    }
    if (Date.now() > lockDeadline) {
      break;
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }

  await fsp.rm(files.pidPath, { force: true });
  const status = await collectStatus(flags);
  status.stoppedPid = stoppedPid;
  status.stopSignal = stopSignal;
  status.forcedStop = forcedStop;
  status.pidFileRemoved = true;
  status.stopReason = stopReason;
  status.message = forcedStop
    ? "Force-stopped the recorded wacli sync owner after the graceful pause timed out."
    : stoppedPid
      ? "Paused the recorded wacli sync owner."
      : "Cleared the recorded wacli sync owner state.";
  return status;
}

function emit(result: LiveStatus, asJson: boolean) {
  if (asJson) {
    console.log(JSON.stringify(result, null, 2));
    return;
  }
  console.log(`message=${result.message}`);
  console.log(`store_dir=${result.storeDir}`);
  console.log(`owner_running=${String(result.ownerRunning)}`);
  console.log(`connected=${String(result.connected)}`);
  console.log(`owner_command_matches=${String(result.ownerCommandMatches)}`);
  console.log(`lock_held_by_owner=${String(result.lockHeldByOwner)}`);
  if (result.ownerPid) {
    console.log(`owner_pid=${String(result.ownerPid)}`);
  }
  if (result.lastLifecycleEvent) {
    console.log(`last_lifecycle_event=${result.lastLifecycleEvent}`);
  }
  if (result.lockInfo) {
    console.log(`lock_info=${result.lockInfo.replace(/\n/g, " | ")}`);
  }
  if (result.stopReason) {
    console.log(`stop_reason=${result.stopReason}`);
  }
  if (result.stoppedPid) {
    console.log(`stopped_pid=${String(result.stoppedPid)}`);
  }
  if (result.stopSignal) {
    console.log(`stop_signal=${result.stopSignal}`);
  }
  if (result.forcedStop) {
    console.log("forced_stop=true");
  }
}

async function main() {
  const flags = parseArgs(process.argv.slice(2));
  let result: LiveStatus;
  switch (flags.command) {
    case "ensure":
      result = await ensureOwner(flags);
      break;
    case "stop":
      result = await stopOwner(flags);
      break;
    case "status":
    default:
      result = await collectStatus(flags);
      break;
  }
  emit(result, flags.json);
  process.exit(result.ok ? 0 : 1);
}

function isEntrypoint(): boolean {
  if (!process.argv[1]) {
    return false;
  }
  try {
    return (
      fs.realpathSync(path.resolve(process.argv[1])) ===
      fs.realpathSync(fileURLToPath(import.meta.url))
    );
  } catch {
    return path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
  }
}

if (isEntrypoint()) {
  main().catch((error) => {
    const scriptName = path.basename(process.argv[1] ?? "wacli-live.ts");
    console.error(`${scriptName}: ${String(error)}`);
    process.exit(1);
  });
}
