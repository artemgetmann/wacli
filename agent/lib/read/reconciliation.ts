import { DatabaseSync } from "node:sqlite";

export type WacliChatRecord = {
  jid: string;
  kind: string;
  name: string | null;
  lastMessageTs: number | null;
};

export type WacliReplyRecord = {
  chatJid: string;
  msgId: string;
  senderJid: string | null;
  ts: number;
  fromMe: boolean;
  text: string | null;
  mediaType: string | null;
  mediaCaption: string | null;
  displayText: string | null;
  chatName: string | null;
  senderName: string | null;
};

export type WacliCandidateChat = {
  jid: string;
  kind: string;
  name: string | null;
  lastMessageTs: number | null;
  reasons: string[];
  score: number;
};

export type WacliLatestInboundReply = WacliReplyRecord & {
  effectiveText: string | null;
  hasRenderableContent: boolean;
};

export type WacliConversationTurn = WacliReplyRecord & {
  direction: "inbound" | "outbound";
  effectiveText: string | null;
  hasRenderableContent: boolean;
};

export type WacliReplyContinuity = {
  contextChatJid: string | null;
  recentTurnCount: number;
  hasPriorInbound: boolean;
  hasPriorOutbound: boolean;
  lastOutboundReply: WacliConversationTurn | null;
  previousOutboundReply: WacliConversationTurn | null;
  lastOutboundNormalizedText: string | null;
  previousOutboundNormalizedText: string | null;
  lastOutboundIsRepeatOfPrevious: boolean;
};

export type WacliReplyLookupResult = {
  target: string;
  seedJids: string[];
  seedPhones: string[];
  identityNames: string[];
  candidates: WacliCandidateChat[];
  seedMessage?: WacliConversationTurn | null;
  latestInboundReply: WacliLatestInboundReply | null;
  recentConversation: WacliConversationTurn[];
  continuity: WacliReplyContinuity;
  preferredMonitorChatJid: string | null;
};

type ContactRecord = {
  jid: string;
  phone: string | null;
  pushName: string | null;
  fullName: string | null;
  firstName: string | null;
  businessName: string | null;
};

type AliasRecord = {
  jid: string;
  alias: string;
};

const WHATSAPP_PN_JID_RE = /^(\d+)(?::\d+)?@s\.whatsapp\.net$/i;

function normalizeDigits(value: string | null | undefined): string | null {
  if (!value) {
    return null;
  }
  const digits = value.replace(/\D/g, "");
  return digits.length > 0 ? digits : null;
}

function normalizeName(value: string | null | undefined): string | null {
  if (!value) {
    return null;
  }
  const normalized = value.trim().replace(/\s+/g, " ").toLowerCase();
  return normalized.length > 0 ? normalized : null;
}

function extractPhoneDigitsFromJid(jid: string | null | undefined): string | null {
  if (!jid) {
    return null;
  }
  const match = jid.match(WHATSAPP_PN_JID_RE);
  return match?.[1] ?? null;
}

function uniqueNonEmpty(values: Array<string | null | undefined>): string[] {
  return [
    ...new Set(
      values.map((value) => value?.trim()).filter((value): value is string => Boolean(value)),
    ),
  ];
}

function buildNameIndex(params: {
  chats: WacliChatRecord[];
  contacts: ContactRecord[];
  aliases: AliasRecord[];
}): Map<string, Set<string>> {
  const index = new Map<string, Set<string>>();
  const add = (rawName: string | null | undefined, jid: string) => {
    const key = normalizeName(rawName);
    if (!key) {
      return;
    }
    let bucket = index.get(key);
    if (!bucket) {
      bucket = new Set<string>();
      index.set(key, bucket);
    }
    bucket.add(jid);
  };

  for (const chat of params.chats) {
    add(chat.name, chat.jid);
  }
  for (const contact of params.contacts) {
    add(contact.pushName, contact.jid);
    add(contact.fullName, contact.jid);
    add(contact.firstName, contact.jid);
    add(contact.businessName, contact.jid);
  }
  for (const alias of params.aliases) {
    add(alias.alias, alias.jid);
  }

  return index;
}

function buildChatMap(chats: WacliChatRecord[]): Map<string, WacliChatRecord> {
  return new Map(chats.map((chat) => [chat.jid, chat]));
}

function buildCandidateScore(reasons: Set<string>): number {
  let score = 0;
  if (reasons.has("exact-jid")) {
    score += 100;
  }
  if (reasons.has("matching-phone")) {
    score += 50;
  }
  if (reasons.has("matching-name")) {
    score += 20;
  }
  return score;
}

function computeEffectiveText(row: WacliReplyRecord): string | null {
  const text = row.text?.trim();
  if (text) {
    return text;
  }
  const caption = row.mediaCaption?.trim();
  if (caption) {
    return caption;
  }
  const display = row.displayText?.trim();
  return display || null;
}

function hasRenderableInboundContent(row: WacliReplyRecord): boolean {
  return Boolean(
    row.text?.trim() ||
    row.mediaCaption?.trim() ||
    row.mediaType?.trim() ||
    row.displayText?.trim(),
  );
}

function toConversationTurn(row: WacliReplyRecord): WacliConversationTurn {
  return {
    ...row,
    fromMe: Boolean(row.fromMe),
    direction: row.fromMe ? "outbound" : "inbound",
    effectiveText: computeEffectiveText(row),
    hasRenderableContent: hasRenderableInboundContent(row),
  };
}

function normalizeComparableReplyText(text: string | null): string | null {
  if (!text) {
    return null;
  }
  const normalized = text
    .toLowerCase()
    .replace(/[’']/g, "")
    .replace(/[^a-z0-9]+/g, " ")
    .trim()
    .replace(/\s+/g, " ");
  return normalized.length > 0 ? normalized : null;
}

function buildReplyContinuity(params: {
  contextChatJid: string | null;
  recentConversation: WacliConversationTurn[];
}): WacliReplyContinuity {
  const outboundTurns = params.recentConversation.filter((turn) => turn.direction === "outbound");
  const lastOutboundReply = outboundTurns.at(-1) ?? null;
  const previousOutboundReply =
    outboundTurns.length >= 2 ? outboundTurns[outboundTurns.length - 2] : null;
  const lastOutboundNormalizedText = normalizeComparableReplyText(
    lastOutboundReply?.effectiveText ?? null,
  );
  const previousOutboundNormalizedText = normalizeComparableReplyText(
    previousOutboundReply?.effectiveText ?? null,
  );

  return {
    contextChatJid: params.contextChatJid,
    recentTurnCount: params.recentConversation.length,
    hasPriorInbound: params.recentConversation.some((turn) => turn.direction === "inbound"),
    hasPriorOutbound: outboundTurns.length > 0,
    lastOutboundReply,
    previousOutboundReply,
    lastOutboundNormalizedText,
    previousOutboundNormalizedText,
    // This catches the exact spam shape from the live trace: a fresh inbound
    // arrived, but the monitor had already sent the same negotiation line on
    // consecutive turns. Wrappers can treat this as a hard "do not send the
    // same thing again" signal.
    lastOutboundIsRepeatOfPrevious:
      Boolean(lastOutboundNormalizedText) &&
      lastOutboundNormalizedText === previousOutboundNormalizedText,
  };
}

/**
 * Read only the local `wacli.db` tables that encode WhatsApp identity hints and
 * turn them into real candidate chat JIDs. The key rule: never fabricate a
 * `<digits>@lid` variant because the actual LID JID can be opaque and unrelated
 * to the phone number digits.
 */
export function findLatestInboundReplyAcrossResolvedChats(params: {
  dbPath: string;
  seedMsgId?: string;
  target: string;
}): WacliReplyLookupResult {
  const db = new DatabaseSync(params.dbPath, { readOnly: true });

  try {
    const chats = db
      .prepare(
        `SELECT jid, kind, name, last_message_ts AS lastMessageTs
         FROM chats`,
      )
      .all() as WacliChatRecord[];
    const chatJids = new Set(chats.map((chat) => chat.jid));
    const contacts = db
      .prepare(
        `SELECT jid,
                phone,
                push_name AS pushName,
                full_name AS fullName,
                first_name AS firstName,
                business_name AS businessName
         FROM contacts`,
      )
      .all() as ContactRecord[];
    const aliases = db
      .prepare(
        `SELECT jid, alias
         FROM contact_aliases`,
      )
      .all() as AliasRecord[];
    const chatMap = buildChatMap(chats);
    const nameIndex = buildNameIndex({ chats, contacts, aliases });

    const rawTarget = params.target.trim();
    const explicitJid = rawTarget.includes("@") ? rawTarget : null;
    // Explicit opaque LIDs do not encode a phone in the JID. Resolve their
    // stored contact phone so identity matching works in both directions while
    // still requiring positive DB evidence.
    const seedPhone = explicitJid
      ? (extractPhoneDigitsFromJid(explicitJid) ??
        normalizeDigits(contacts.find((contact) => contact.jid === explicitJid)?.phone))
      : normalizeDigits(rawTarget);
    const seedJids = new Set<string>();
    if (explicitJid) {
      seedJids.add(explicitJid);
    }

    const candidateReasons = new Map<string, Set<string>>();
    const addCandidate = (jid: string, reason: string) => {
      let reasons = candidateReasons.get(jid);
      if (!reasons) {
        reasons = new Set<string>();
        candidateReasons.set(jid, reasons);
      }
      reasons.add(reason);
    };

    for (const jid of seedJids) {
      addCandidate(jid, "exact-jid");
    }

    // Phone matches come from stored data only: contact rows and canonical phone JIDs.
    if (seedPhone) {
      for (const contact of contacts) {
        const contactPhone = normalizeDigits(contact.phone);
        if (contactPhone === seedPhone) {
          addCandidate(contact.jid, "matching-phone");
          seedJids.add(contact.jid);
        }
      }
      for (const chat of chats) {
        if (extractPhoneDigitsFromJid(chat.jid) === seedPhone) {
          addCandidate(chat.jid, chat.jid === explicitJid ? "exact-jid" : "matching-phone");
          seedJids.add(chat.jid);
        }
      }
    }

    const exactMessages =
      seedJids.size > 0
        ? (db
            .prepare(
              `SELECT DISTINCT chat_jid AS chatJid, chat_name AS chatName, sender_name AS senderName
               FROM messages
               WHERE chat_jid IN (${[...seedJids].map(() => "?").join(",")})`,
            )
            .all(...seedJids) as Array<{
            chatJid: string;
            chatName: string | null;
            senderName: string | null;
          }>)
        : [];

    const identityNames = new Set<string>();
    const addIdentityName = (value: string | null | undefined) => {
      const normalized = normalizeName(value);
      if (normalized) {
        identityNames.add(normalized);
      }
    };

    for (const jid of seedJids) {
      const chat = chatMap.get(jid);
      addIdentityName(chat?.name);
      const contact = contacts.find((row) => row.jid === jid);
      addIdentityName(contact?.pushName);
      addIdentityName(contact?.fullName);
      addIdentityName(contact?.firstName);
      addIdentityName(contact?.businessName);
      const alias = aliases.find((row) => row.jid === jid);
      addIdentityName(alias?.alias);
    }
    for (const row of exactMessages) {
      addIdentityName(row.chatName);
      addIdentityName(row.senderName);
    }

    // A shared display name is not identity evidence. Name expansion may only
    // add a candidate when that candidate's canonical JID or stored contact
    // phone positively maps back to the target phone. This preserves opaque
    // @lid reconciliation without letting unknown-phone LIDs claim replies.
    for (const name of identityNames) {
      const matchingJids = nameIndex.get(name) ?? new Set<string>();
      for (const jid of matchingJids) {
        const candidatePhone =
          extractPhoneDigitsFromJid(jid) ??
          normalizeDigits(contacts.find((contact) => contact.jid === jid)?.phone);
        if (!seedPhone || candidatePhone !== seedPhone) {
          continue;
        }
        addCandidate(jid, "matching-name");
      }
    }

    let candidates = [...candidateReasons.entries()]
      .filter(([jid]) => chatJids.has(jid))
      .map(([jid, reasons]) => {
        const chat = chatMap.get(jid);
        return {
          jid,
          kind: chat?.kind ?? "unknown",
          name: chat?.name ?? null,
          lastMessageTs: chat?.lastMessageTs ?? null,
          reasons: [...reasons].toSorted(),
          score: buildCandidateScore(reasons),
        } satisfies WacliCandidateChat;
      })
      .toSorted((left, right) => {
        return (
          right.score - left.score ||
          (right.lastMessageTs ?? 0) - (left.lastMessageTs ?? 0) ||
          left.jid.localeCompare(right.jid)
        );
      });

    let seedMessage: WacliConversationTurn | null = null;
    let latestInboundReply: WacliLatestInboundReply | null = null;
    if (candidates.length > 0) {
      const candidateJids = candidates.map((candidate) => candidate.jid);
      const requestedSeedMsgId = params.seedMsgId?.trim();
      if (requestedSeedMsgId) {
        const seedRows = db
          .prepare(
            `SELECT chat_jid AS chatJid,
                    msg_id AS msgId,
                    sender_jid AS senderJid,
                    ts,
                    from_me AS fromMe,
                    text,
                    media_type AS mediaType,
                    media_caption AS mediaCaption,
                    display_text AS displayText,
                    chat_name AS chatName,
                    sender_name AS senderName
             FROM messages
             WHERE msg_id = ?
               AND chat_jid IN (${candidateJids.map(() => "?").join(",")})
             ORDER BY ts DESC, rowid DESC
             LIMIT 1`,
          )
          .all(requestedSeedMsgId, ...candidateJids) as unknown as WacliReplyRecord[];
        seedMessage = seedRows[0] ? toConversationTurn(seedRows[0]) : null;
      }

      const inbound = db
        .prepare(
          `SELECT chat_jid AS chatJid,
                  msg_id AS msgId,
                  sender_jid AS senderJid,
                  ts,
                  from_me AS fromMe,
                  text,
                  media_type AS mediaType,
                  media_caption AS mediaCaption,
                  display_text AS displayText,
                  chat_name AS chatName,
                  sender_name AS senderName
           FROM messages
           WHERE from_me = 0
             AND chat_jid IN (${candidateJids.map(() => "?").join(",")})
           ORDER BY ts DESC`,
        )
        .all(...candidateJids) as unknown as WacliReplyRecord[];

      const latest = inbound.find((row) => hasRenderableInboundContent(row)) ?? null;
      if (latest) {
        const latestTurn = toConversationTurn(latest);
        latestInboundReply = {
          ...latestTurn,
          effectiveText: latestTurn.effectiveText,
          hasRenderableContent: latestTurn.hasRenderableContent,
        };

        // Root cause: monitor setup was pinning whichever candidate sorted first,
        // which biased exact phone JIDs over the real active sibling @lid thread.
        // Once we know which candidate owns the newest actionable inbound
        // message, that chat must become the preferred monitor target.
        candidates = candidates
          .map((candidate) => {
            if (candidate.jid !== latest.chatJid) {
              return candidate;
            }
            const reasons = new Set(candidate.reasons);
            reasons.add("active-inbound-thread");
            return {
              ...candidate,
              reasons: [...reasons].toSorted(),
              score: candidate.score + 200,
            } satisfies WacliCandidateChat;
          })
          .toSorted((left, right) => {
            return (
              right.score - left.score ||
              (right.lastMessageTs ?? 0) - (left.lastMessageTs ?? 0) ||
              left.jid.localeCompare(right.jid)
            );
          });
      }
    }

    const preferredMonitorChatJid = latestInboundReply?.chatJid ?? candidates[0]?.jid ?? null;
    const recentConversation =
      preferredMonitorChatJid === null
        ? []
        : (
            db
              .prepare(
                `SELECT chat_jid AS chatJid,
                      msg_id AS msgId,
                      sender_jid AS senderJid,
                      ts,
                      from_me AS fromMe,
                      text,
                      media_type AS mediaType,
                      media_caption AS mediaCaption,
                      display_text AS displayText,
                      chat_name AS chatName,
                      sender_name AS senderName
               FROM messages
               WHERE chat_jid = ?
               ORDER BY ts DESC, rowid DESC
               LIMIT 6`,
              )
              .all(preferredMonitorChatJid) as unknown as WacliReplyRecord[]
          )
            .map((row) => toConversationTurn(row))
            .toReversed();
    const continuity = buildReplyContinuity({
      contextChatJid: preferredMonitorChatJid,
      recentConversation,
    });

    return {
      target: rawTarget,
      seedJids: [...seedJids].toSorted(),
      seedPhones: uniqueNonEmpty([seedPhone]),
      identityNames: [...identityNames].toSorted(),
      candidates,
      seedMessage,
      latestInboundReply,
      recentConversation,
      continuity,
      preferredMonitorChatJid,
    };
  } finally {
    db.close();
  }
}
