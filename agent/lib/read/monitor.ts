import type { WacliReplyLookupResult } from "./reconciliation.ts";
import { createHash } from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";

export type MonitorState = {
  lastProcessedMsgId?: string;
  msgId?: string;
  ts?: number;
  target?: string;
  dbPath?: string;
  chatJid?: string;
  ackTokenHash?: string;
};

export type MonitorAckSnapshot = {
  version: 1;
  target: string;
  dbPath: string;
  stateFile: string;
  previousStateHash: string;
  chatJid: string;
  msgId: string;
};

// Hash the entire baseline, including its absence. A late acknowledgement must
// not overwrite a different worker's completed work, even if IDs happen to match.
export async function readMonitorState(stateFile: string): Promise<{
  state: MonitorState | null;
  hash: string;
}> {
  let raw: string | null;
  try {
    raw = await fs.readFile(stateFile, "utf8");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    raw = null;
  }
  const state = raw === null ? null : JSON.parse(raw) as MonitorState;
  if (state !== null && (typeof state !== "object" || Array.isArray(state))) {
    throw new Error("Invalid monitor state: expected an object");
  }
  return {
    state,
    hash: createHash("sha256").update(raw === null ? "missing" : `present:${raw}`).digest("hex"),
  };
}

export function verifyMonitorScope(state: MonitorState | null, target: string, dbPath: string): void {
  // Old baselines contain only msgId/lastProcessedMsgId and ts. Keep those
  // readable, then bind their scope when the first explicit ack succeeds.
  if ((state?.target !== undefined && state.target !== target.trim()) ||
      (state?.dbPath !== undefined && state.dbPath !== path.resolve(dbPath))) {
    throw new Error("Monitor state belongs to a different target or database");
  }
}

export function encodeMonitorAckSnapshot(snapshot: MonitorAckSnapshot): string {
  // This is a caller receipt, not a credential. Passing it back makes the exact
  // checked message and previous baseline explicit without writing during check.
  return Buffer.from(JSON.stringify(snapshot)).toString("base64url");
}

export function decodeMonitorAckSnapshot(token: string): MonitorAckSnapshot {
  try {
    const value = JSON.parse(Buffer.from(token, "base64url").toString("utf8"));
    if (value.version !== 1 || !["target", "dbPath", "stateFile", "previousStateHash", "chatJid", "msgId"]
      .every((key) => typeof value[key] === "string" && value[key].length > 0) ||
      !/^[a-f0-9]{64}$/.test(value.previousStateHash)) throw new Error();
    return value as MonitorAckSnapshot;
  } catch {
    throw new Error("Invalid monitor acknowledgement token");
  }
}

export type WacliMonitorBootstrapDecision =
  | {
      action: "process-latest";
      chatJid: string;
      msgId: string;
      reason:
        | "first-run-actionable-latest"
        | "new-actionable-latest"
        | "single-candidate-actionable-latest";
    }
  | {
      action: "noop";
      chatJid: string | null;
      msgId: string | null;
      reason: "no-actionable-latest" | "already-processed";
    };

export function resolvePreferredMonitorChatJid(result: WacliReplyLookupResult): string {
  if (result.latestInboundReply) {
    const matchedCandidate = result.candidates.find(
      (candidate) => candidate.jid === result.latestInboundReply?.chatJid,
    );
    if (!matchedCandidate) {
      throw new Error(
        `Unsafe WhatsApp monitor resolution for ${result.target}: latest inbound reply chat ${result.latestInboundReply.chatJid} is not a resolved candidate.`,
      );
    }
    return matchedCandidate.jid;
  }

  if (result.candidates.length === 1) {
    return result.candidates[0].jid;
  }

  // Root cause: when the phone JID and sibling @lid chat both exist but no
  // actionable inbound row identifies the active thread, pinning "the first
  // candidate" is unsafe. Fail loudly instead of silently monitoring the wrong
  // conversation.
  throw new Error(
    `Unsafe WhatsApp monitor resolution for ${result.target}: ${result.candidates.length} candidate chats but no actionable latest inbound message to identify the active thread.`,
  );
}

export function decideWacliMonitorBootstrapAction(params: {
  lastProcessedMsgId?: string | null;
  lookup: WacliReplyLookupResult;
}): WacliMonitorBootstrapDecision {
  const chatJid = resolvePreferredMonitorChatJid(params.lookup);
  const latest = params.lookup.latestInboundReply;
  if (!latest) {
    return {
      action: "noop",
      chatJid,
      msgId: null,
      reason: "no-actionable-latest",
    };
  }

  if (params.lastProcessedMsgId && params.lastProcessedMsgId === latest.msgId) {
    return {
      action: "noop",
      chatJid,
      msgId: latest.msgId,
      reason: "already-processed",
    };
  }

  // Root cause: the old bootstrap pattern stored the first seen inbound msgId
  // as a baseline and skipped it. That silently drops the exact actionable
  // latest message the monitor was started to catch. First run should process
  // that latest message, then persist the msgId only after the action succeeds.
  return {
    action: "process-latest",
    chatJid,
    msgId: latest.msgId,
    reason: params.lastProcessedMsgId
      ? "new-actionable-latest"
      : params.lookup.candidates.length === 1
        ? "single-candidate-actionable-latest"
        : "first-run-actionable-latest",
  };
}
