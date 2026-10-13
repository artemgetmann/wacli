import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

type Flags = {
  json: boolean;
  refresh: boolean;
  ensureOwner: boolean;
  store?: string;
  timeoutMs: number;
  idleExit: string;
};

type CommandResult = {
  ok: boolean;
  exitCode: number | null;
  stdout: string;
  stderr: string;
  timedOut: boolean;
};

type DoctorJson = {
  success?: boolean;
  data?: {
    store_dir?: string;
    lock_held?: boolean;
    lock_info?: string;
    authenticated?: boolean;
    connected?: boolean;
    fts_enabled?: boolean;
  };
  error?: string | null;
};

type ChatsJson = {
  success?: boolean;
  data?: Array<{
    JID?: string;
    Kind?: string;
    Name?: string;
    LastMessageTS?: string;
  }>;
  error?: string | null;
};

type RefreshJson = {
  success?: boolean;
  data?: {
    synced?: boolean;
    messages_stored?: number;
  };
  error?: string | null;
};

type LiveOwnerStatus = {
  ok?: boolean;
  ownerRunning?: boolean;
  ownerPid?: number;
  ownerCommandMatches?: boolean;
  lockHeldByOwner?: boolean;
  lastLifecycleEvent?: "connected" | "disconnected" | "reconnecting" | "stopping" | "unknown";
  connected?: boolean;
  timedOut?: boolean;
  message?: string;
};

type HealthStatus =
  | "healthy"
  | "healthy_after_refresh"
  | "paired_not_connected_readable"
  | "paired_not_connected_refresh_failed"
  | "not_authenticated"
  | "locked"
  | "empty_history"
  | "probe_failed";

type HealthReport = {
  status: HealthStatus;
  message: string;
  storeDir?: string;
  authenticated?: boolean;
  connected?: boolean;
  lockHeld?: boolean;
  lockInfo?: string;
  chatsReadable: boolean;
  sampleChatCount: number;
  refreshAttempted: boolean;
  refreshSucceeded: boolean;
  refreshTimedOut: boolean;
  owner?: LiveOwnerStatus;
  doctor?: DoctorJson;
  chats?: ChatsJson;
  refresh?: RefreshJson;
};

export function resolveHealthProbePlan(
  connected: boolean,
  refreshRequested: boolean,
  ownerTimedOut: boolean,
): { status: HealthStatus; refreshAttempted: boolean } {
  if (ownerTimedOut) {
    // Refresh cannot repair or explain a timed-out owner orchestration probe.
    // Preserve that stronger failure instead of letting a later one-shot sync
    // overwrite it with an unrelated connected/disconnected status.
    return { status: "probe_failed", refreshAttempted: false };
  }
  return {
    status: connected ? "healthy" : "paired_not_connected_readable",
    refreshAttempted: refreshRequested,
  };
}

function usage() {
  console.error(`Usage:
  wacli agent health [--json] [--refresh] [--ensure-owner] [--store <dir>] [--timeout-ms <ms>] [--idle-exit <duration>]

Notes:
  - Runs bounded health checks for wacli without using bare 'wacli sync --json'.
  - --ensure-owner starts or reuses a managed 'wacli sync --follow' process.
  - --refresh adds a one-shot sync using 'wacli sync --once --idle-exit <duration> --json'.`);
}

function parseArgs(argv: string[]): Flags {
  const flags: Flags = {
    json: false,
    refresh: false,
    ensureOwner: false,
    timeoutMs: 15_000,
    idleExit: "5s",
  };

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    switch (arg) {
      case "--json":
        flags.json = true;
        break;
      case "--refresh":
        flags.refresh = true;
        break;
      case "--ensure-owner":
        flags.ensureOwner = true;
        break;
      case "--store": {
        const next = argv[index + 1];
        if (!next) {
          throw new Error("--store requires a value");
        }
        flags.store = next;
        index += 1;
        break;
      }
      case "--timeout-ms": {
        const next = argv[index + 1];
        if (!next) {
          throw new Error("--timeout-ms requires a value");
        }
        const parsed = Number.parseInt(next, 10);
        if (!Number.isFinite(parsed) || parsed <= 0) {
          throw new Error(`Invalid --timeout-ms: ${next}`);
        }
        flags.timeoutMs = parsed;
        index += 1;
        break;
      }
      case "--idle-exit": {
        const next = argv[index + 1];
        if (!next) {
          throw new Error("--idle-exit requires a value");
        }
        flags.idleExit = next;
        index += 1;
        break;
      }
      case "-h":
      case "--help":
        usage();
        process.exit(0);
      default:
        throw new Error(`Unexpected argument: ${arg}`);
    }
  }

  return flags;
}

function buildBaseArgs(flags: Flags) {
  return flags.store ? ["--store", flags.store] : [];
}

// Keep each wacli probe bounded so callers can distinguish real state from a
// command form that would otherwise run forever.
async function runCommand(
  command: string,
  args: string[],
  timeoutMs: number,
): Promise<CommandResult> {
  return await new Promise((resolve) => {
    const child = spawn(command, args, {
      stdio: ["ignore", "pipe", "pipe"],
      env: process.env,
    });

    let stdout = "";
    let stderr = "";
    let timedOut = false;

    const timer = setTimeout(() => {
      timedOut = true;
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
      resolve({
        ok: !timedOut && exitCode === 0,
        exitCode,
        stdout,
        stderr,
        timedOut,
      });
    });
  });
}

function parseJson<T>(raw: string): T | undefined {
  const trimmed = raw.trim();
  if (!trimmed) {
    return undefined;
  }
  return JSON.parse(trimmed) as T;
}

function containsLockError(raw: string) {
  return raw.toLowerCase().includes("store is locked");
}

export function resolveLiveOwnerHelperTimeoutMs(
  timeoutMs: number,
  action: "status" | "ensure",
): number {
  // Ensure can consume one reconnect grace, one graceful stop, and one fresh
  // settle window. Keep status at the ordinary probe budget because it never
  // mutates or waits for lifecycle transitions.
  return action === "ensure" ? Math.max(timeoutMs * 2 + 15_000, 20_000) : timeoutMs;
}

async function readLiveOwnerStatus(
  flags: Flags,
  action: "status" | "ensure",
): Promise<LiveOwnerStatus | undefined> {
  const helperPath = path.join(path.dirname(fileURLToPath(import.meta.url)), "live.ts");
  const args = [action, "--json"];
  if (flags.store) {
    args.push("--store", flags.store);
  }
  args.push("--timeout-ms", String(Math.min(flags.timeoutMs, 2_000)));
  if (action === "ensure") {
    // Give the live-owner helper the caller's normal probe window to establish
    // a replacement, while keeping its graceful stop bounded to five seconds.
    args.push("--settle-ms", String(flags.timeoutMs));
    args.push("--grace-ms", String(Math.min(flags.timeoutMs, 5_000)));
  }

  // Recovery can include reconnect grace, a graceful stop, and a fresh settle
  // window. The old single-probe timeout killed the helper midway through that
  // sequence and discarded its honest final status.
  const helperTimeoutMs = resolveLiveOwnerHelperTimeoutMs(flags.timeoutMs, action);
  const result = await runCommand(
    process.execPath,
    ["--experimental-strip-types", helperPath, ...args],
    Math.max(helperTimeoutMs, 5_000),
  );
  if (result.timedOut) {
    return {
      ok: false,
      connected: false,
      timedOut: true,
      message: `Wacli live-owner ${action} timed out after ${helperTimeoutMs}ms.`,
    };
  }
  if (!result.stdout.trim()) {
    return undefined;
  }
  return parseJson<LiveOwnerStatus>(result.stdout);
}

function buildMessage(report: HealthReport) {
  switch (report.status) {
    case "healthy":
      return "WhatsApp CLI is connected and chat history is readable.";
    case "healthy_after_refresh":
      return "WhatsApp CLI is connected and the bounded refresh completed.";
    case "paired_not_connected_readable":
      return "WhatsApp CLI is paired and chat history is readable, but live sync is not connected.";
    case "paired_not_connected_refresh_failed":
      return "WhatsApp CLI is paired and chat history is readable, but the bounded refresh did not restore a live connection.";
    case "not_authenticated":
      return "WhatsApp CLI is not paired yet.";
    case "locked":
      return "WhatsApp CLI store is locked by another process.";
    case "empty_history":
      return "WhatsApp CLI is paired, but no chat history is available yet.";
    case "probe_failed":
    default:
      return "WhatsApp CLI health probe failed.";
  }
}

function emit(report: HealthReport, asJson: boolean) {
  if (asJson) {
    console.log(JSON.stringify(report, null, 2));
    return;
  }

  console.log(`status=${report.status}`);
  console.log(`message=${report.message}`);
  console.log(`authenticated=${String(report.authenticated ?? false)}`);
  console.log(`connected=${String(report.connected ?? false)}`);
  console.log(`chats_readable=${String(report.chatsReadable)}`);
  console.log(`sample_chat_count=${String(report.sampleChatCount)}`);
  if (report.storeDir) {
    console.log(`store_dir=${report.storeDir}`);
  }
  if (report.lockInfo) {
    console.log(`lock_info=${report.lockInfo.replace(/\n/g, " | ")}`);
  }
}

async function main() {
  const flags = parseArgs(process.argv.slice(2));
  const baseArgs = buildBaseArgs(flags);
  let owner = await readLiveOwnerStatus(flags, "status");

  const doctorRun = await runCommand(
    process.env.WACLI_AGENT_BINARY || "wacli",
    [...baseArgs, "--read-only", "doctor", "--json"],
    flags.timeoutMs,
  );
  const doctor =
    doctorRun.ok || doctorRun.stdout.trim() ? parseJson<DoctorJson>(doctorRun.stdout) : undefined;
  const doctorRaw = `${doctorRun.stdout}\n${doctorRun.stderr}`;

  if (doctorRun.timedOut) {
    emit(
      {
        status: "probe_failed",
        message: "WhatsApp CLI doctor timed out.",
        chatsReadable: false,
        sampleChatCount: 0,
        refreshAttempted: false,
        refreshSucceeded: false,
        refreshTimedOut: false,
      },
      flags.json,
    );
    return;
  }

  if (containsLockError(doctorRaw)) {
    emit(
      {
        status: "locked",
        message: "WhatsApp CLI store is locked by another process.",
        storeDir: doctor?.data?.store_dir,
        authenticated: doctor?.data?.authenticated,
        connected: doctor?.data?.connected,
        lockHeld: doctor?.data?.lock_held,
        lockInfo: doctor?.data?.lock_info,
        chatsReadable: false,
        sampleChatCount: 0,
        refreshAttempted: false,
        refreshSucceeded: false,
        refreshTimedOut: false,
        owner,
        doctor,
      },
      flags.json,
    );
    return;
  }

  const authenticated = doctor?.data?.authenticated === true;
  if (authenticated && flags.ensureOwner) {
    owner = await readLiveOwnerStatus(flags, "ensure");
  }
  const connected = owner?.connected === true || doctor?.data?.connected === true;

  if (!authenticated) {
    emit(
      {
        status: "not_authenticated",
        message: "WhatsApp CLI is not paired yet.",
        storeDir: doctor?.data?.store_dir,
        authenticated,
        connected,
        lockHeld: doctor?.data?.lock_held,
        lockInfo: doctor?.data?.lock_info,
        chatsReadable: false,
        sampleChatCount: 0,
        refreshAttempted: false,
        refreshSucceeded: false,
        refreshTimedOut: false,
        owner,
        doctor,
      },
      flags.json,
    );
    return;
  }

  const chatsRun = await runCommand(
    process.env.WACLI_AGENT_BINARY || "wacli",
    [...baseArgs, "--read-only", "chats", "list", "--limit", "5", "--json"],
    flags.timeoutMs,
  );
  const chats =
    chatsRun.ok || chatsRun.stdout.trim() ? parseJson<ChatsJson>(chatsRun.stdout) : undefined;
  const chatsRaw = `${chatsRun.stdout}\n${chatsRun.stderr}`;

  if (containsLockError(chatsRaw)) {
    emit(
      {
        status: "locked",
        message: "WhatsApp CLI store is locked by another process.",
        storeDir: doctor?.data?.store_dir,
        authenticated,
        connected,
        lockHeld: doctor?.data?.lock_held,
        lockInfo: doctor?.data?.lock_info,
        chatsReadable: false,
        sampleChatCount: 0,
        refreshAttempted: false,
        refreshSucceeded: false,
        refreshTimedOut: false,
        owner,
        doctor,
        chats,
      },
      flags.json,
    );
    return;
  }

  const chatsReadable = chats?.success === true;
  const sampleChatCount = Array.isArray(chats?.data) ? chats.data.length : 0;

  if (!chatsReadable) {
    emit(
      {
        status: "probe_failed",
        message: "WhatsApp CLI chat probe failed.",
        storeDir: doctor?.data?.store_dir,
        authenticated,
        connected,
        lockHeld: doctor?.data?.lock_held,
        lockInfo: doctor?.data?.lock_info,
        chatsReadable,
        sampleChatCount,
        refreshAttempted: false,
        refreshSucceeded: false,
        refreshTimedOut: false,
        owner,
        doctor,
        chats,
      },
      flags.json,
    );
    return;
  }

  if (sampleChatCount === 0) {
    emit(
      {
        status: "empty_history",
        message: "WhatsApp CLI is paired, but no chat history is available yet.",
        storeDir: doctor?.data?.store_dir,
        authenticated,
        connected,
        lockHeld: doctor?.data?.lock_held,
        lockInfo: doctor?.data?.lock_info,
        chatsReadable,
        sampleChatCount,
        refreshAttempted: false,
        refreshSucceeded: false,
        refreshTimedOut: false,
        owner,
        doctor,
        chats,
      },
      flags.json,
    );
    return;
  }

  let refreshRun: CommandResult | undefined;
  let refresh: RefreshJson | undefined;
  const probePlan = resolveHealthProbePlan(connected, flags.refresh, owner?.timedOut === true);
  let status = probePlan.status;

  if (probePlan.refreshAttempted) {
    // One-shot sync only: explicit once mode plus explicit idle exit. This is
    // the safe replacement for the old bare 'wacli sync --json' probe.
    refreshRun = await runCommand(
      process.env.WACLI_AGENT_BINARY || "wacli",
      [...baseArgs, "sync", "--once", "--idle-exit", flags.idleExit, "--json"],
      flags.timeoutMs,
    );
    refresh =
      refreshRun.ok || refreshRun.stdout.trim()
        ? parseJson<RefreshJson>(refreshRun.stdout)
        : undefined;
    const refreshRaw = `${refreshRun.stdout}\n${refreshRun.stderr}`;

    if (containsLockError(refreshRaw)) {
      status = owner?.connected === true ? "healthy" : "locked";
    } else if (refreshRun.ok && refresh?.success === true) {
      status = connected ? "healthy_after_refresh" : "paired_not_connected_refresh_failed";
    } else if (!connected && (refreshRun.timedOut || refresh?.success === false)) {
      status = "paired_not_connected_refresh_failed";
    }
  }

  const report: HealthReport = {
    status,
    message: "",
    storeDir: doctor?.data?.store_dir,
    authenticated,
    connected,
    lockHeld: doctor?.data?.lock_held,
    lockInfo: doctor?.data?.lock_info,
    chatsReadable,
    sampleChatCount,
    refreshAttempted: probePlan.refreshAttempted,
    refreshSucceeded: Boolean(refreshRun?.ok && refresh?.success === true),
    refreshTimedOut: refreshRun?.timedOut === true,
    owner,
    doctor,
    chats,
    refresh,
  };
  report.message = buildMessage(report);
  if (owner?.timedOut) {
    report.message = owner.message ?? "WhatsApp CLI live-owner recovery timed out.";
  }
  emit(report, flags.json);
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
    const scriptName = path.basename(process.argv[1] ?? "wacli-health.ts");
    console.error(`${scriptName}: ${String(error)}`);
    process.exit(1);
  });
}
