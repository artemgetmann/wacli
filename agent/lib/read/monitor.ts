import type { WacliReplyLookupResult } from "./reconciliation.ts";

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
