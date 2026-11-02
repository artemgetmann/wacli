/** Developer-only draft policy. No account, assistant, network or storage access. */
import { createHash } from 'node:crypto';
import { readFileSync, readSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

export const CONTRACT_VERSION = 1;
export const POLICY_VERSION = '1.1.0';
const repository = 'artemgetmann/wacli';
// Match the original Python policy for JSON truth values, including empty containers.
const truth = (value) => !!value && (!(Array.isArray(value) || typeof value === 'object') || Object.keys(value).length > 0);
function reviewContext(review, allowReady = false) {
  if (!review || typeof review !== 'object' || Array.isArray(review)) throw new Error('review evidence object required');
  if (review.repository !== repository) return ['hold', 'wrong_publication_repository'];
  if (!['not_attempted', 'verified'].includes(review.publication_outcome)) return ['hold', 'reconcile_publication_before_retry'];
  if (!truth(review.pr_inventory_complete)) return ['hold', 'deduplication_inventory_incomplete'];
  const prs = review.existing_prs;
  if (!Array.isArray(prs) || prs.some((pr) => !pr || typeof pr !== 'object' || Array.isArray(pr))) throw new Error('bounded authoritative PR inventory required');
  if (prs.length > 64) throw new Error('PR inventory budget exceeded');
  const matches = prs.filter((pr) => pr.finding_id === review.finding_id || pr.equivalent_intent === true);
  if (matches.length > 1) return ['hold', 'multiple_matching_prs_reconcile'];
  if (truth(review.overlapping_owner)) return ['hold', 'existing_owner_retains_work'];
  if (truth(review.decision_only) || truth(review.ancestry_pending)) return ['hold', 'decision_or_ancestry_pending'];
  if (matches.length) {
    const pr = matches[0];
    if (pr.repository !== repository || !truth(pr.remote_verified) || typeof pr.url !== 'string' || !/^https:\/\/github\.com\/artemgetmann\/wacli\/pull\/[1-9][0-9]*(?![\s\S])/.test(pr.url)) return ['hold', 'matching_pr_remote_state_unverified'];
    if (pr.state !== 'open') return ['hold', 'closed_pr_disposition_requires_reconciliation'];
    if (pr.owned !== true || (pr.draft !== true && !(allowReady && pr.draft === false))) return ['hold', 'existing_pr_not_owned_draft'];
  } else if (review.publication_outcome === 'verified') return ['hold', 'verified_publication_missing_from_inventory'];
  return { pr: matches[0] };
}
function changeProof(review) {
  if (['concrete_change', 'missing_from_fork', 'isolated_tests_passed'].some((key) => review[key] !== true)) return ['hold', 'concrete_isolated_change_not_proven'];
  if (typeof review.tested_head !== 'string' || !/^[0-9a-f]{40}(?![\s\S])/.test(review.tested_head)) return ['hold', 'exact_tested_head_required'];
  if (!Array.isArray(review.test_evidence) || !review.test_evidence.length || review.test_evidence.some((v) => typeof v !== 'string' || !v) || ['risks', 'rollout'].some((key) => typeof review[key] !== 'string' || !review[key])) return ['hold', 'tests_risks_rollout_required'];
  return null;
}
export function draftDecision(review) {
  const context = reviewContext(review);
  if (Array.isArray(context)) return context;
  return changeProof(review) || (context.pr ? ['update_existing_draft', 'owned_existing_draft'] : ['create_draft', 'tested_missing_change']);
}
const sha = (value) => typeof value === 'string' && /^[0-9a-f]{40}(?![\s\S])/.test(value);
const refs = (value) => Array.isArray(value) && value.length > 0 && value.length <= 64 && value.every((v) => typeof v === 'string' && v.trim().length > 0 && v.length <= 2048);
const actor = (value) => typeof value === 'string' && value.trim().length > 0 && value.length <= 300;
function findingId(review) {
  if (typeof review?.finding_id !== 'string' || !review.finding_id || review.finding_id.length > 1024) throw new Error('stable finding ID required');
}
export const MAX_PROMOTION_INVENTORY_AGE_MS = 300000;
function instant(value) {
  if (typeof value !== 'string') return NaN;
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,3})?(?:Z|([+-])(\d{2}):(\d{2}))(?![\s\S])/.exec(value);
  if (!m) return NaN;
  const [year, month, day, hour, minute, second] = m.slice(1, 7).map(Number);
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
  if (year < 1 || month < 1 || month > 12 || day < 1 || day > days[month - 1] || hour > 23 || minute > 59 || second > 59 || (m[7] && (Number(m[8]) > 23 || Number(m[9]) > 59))) return NaN;
  return Date.parse(value);
}
export function promotionDecision(review) {
  findingId(review);
  if (!review.finding_id.trim()) throw new Error('stable finding ID required');
  const context = reviewContext(review, true);
  if (Array.isArray(context)) return context;
  if (review.pr_inventory_complete !== true) return ['hold', 'deduplication_inventory_incomplete'];
  const pr = context.pr;
  if (!pr) return ['hold', 'matching_pr_required_for_promotion'];
  if (pr.remote_verified !== true) return ['hold', 'matching_pr_remote_state_unverified'];
  const now = instant(review.decision_at), observed = instant(review.pr_inventory_observed_at);
  if (!Number.isFinite(now) || !Number.isFinite(observed) || observed > now || now - observed > MAX_PROMOTION_INVENTORY_AGE_MS || !refs(review.remote_inventory_evidence)) return ['hold', 'remote_inventory_not_fresh'];
  if (review.owner_overlap_checked !== true) return ['hold', 'owner_overlap_not_verified'];
  const proof = changeProof(review);
  if (proof) return proof;
  if (review.implementation_complete !== true) return ['hold', 'implementation_incomplete'];
  if (review.applicable_tests_passed !== true) return ['hold', 'applicable_tests_incomplete'];
  if (!sha(pr.head_sha) || !sha(review.independent_review_head) || pr.head_sha !== review.tested_head || pr.head_sha !== review.independent_review_head) return ['hold', 'promotion_heads_do_not_match'];
  if (review.independent_review_passed !== true || !refs(review.independent_review_evidence)) return ['hold', 'independent_review_incomplete'];
  if (!actor(review.implementation_owner) || !actor(review.independent_reviewer) || review.implementation_owner.trim().toLowerCase() === review.independent_reviewer.trim().toLowerCase()) return ['hold', 'independent_reviewer_required'];
  if (!Array.isArray(review.unresolved_findings) || review.unresolved_findings.length) return ['hold', 'unresolved_review_findings'];
  return pr.draft === false ? ['already_ready', 'verified_ready_no_transition'] : ['mark_ready', 'exact_head_implementation_tests_review_complete'];
}
export function promotionReport(review) {
  const [promotion_decision, reason] = promotionDecision(review);
  const pr = promotion_decision === 'hold' ? null : review.existing_prs.find((p) => p.finding_id === review.finding_id || p.equivalent_intent === true);
  return { contract_version: CONTRACT_VERSION, policy_version: POLICY_VERSION,
    producer_sha256: createHash('sha256').update(readFileSync(new URL(import.meta.url))).digest('hex'),
    operation: 'promotion', finding_id: review.finding_id, promotion_decision, reason,
    pr_url: pr?.url ?? null, reviewed_head: pr?.head_sha ?? null, publication_performed: false };
}
export function report(review) {
  findingId(review);
  const [decision, reason] = draftDecision(review);
  return { contract_version: CONTRACT_VERSION, policy_version: POLICY_VERSION,
    producer_sha256: createHash('sha256').update(readFileSync(new URL(import.meta.url))).digest('hex'),
    finding_id: review.finding_id, decision, reason };
}
export const MAX_REVIEW_BYTES = 262144;
export function readReviewInput(fd = 0, read = readSync) {
  // Stop at limit+1 even if a writer never closes its stream. No EOF-sized buffer.
  const buffer = Buffer.alloc(MAX_REVIEW_BYTES + 1);
  let size = 0;
  while (size < buffer.length) {
    const count = read(fd, buffer, size, buffer.length - size, null);
    if (count === 0) return buffer.subarray(0, size);
    size += count;
  }
  throw new Error('review budget exceeded');
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const input = readReviewInput();
    const request = JSON.parse(input.toString('utf8'));
    if (request?.operation !== undefined && request.operation !== 'promotion') throw new Error('unsupported operation');
    console.log(JSON.stringify(request?.operation === 'promotion' ? promotionReport(request.review) : report(request)));
  } catch (error) {
    console.log(JSON.stringify({ status: 'hold_policy_input_unavailable', error: error.message }));
    process.exitCode = 2;
  }
}
