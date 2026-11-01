/** Developer-only draft policy. No account, assistant, network or storage access. */
import { createHash } from 'node:crypto';
import { readFileSync, readSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

export const CONTRACT_VERSION = 1;
export const POLICY_VERSION = '1.0.0';
const repository = 'artemgetmann/wacli';
// Match the original Python policy for JSON truth values, including empty containers.
const truth = (value) => !!value && (!(Array.isArray(value) || typeof value === 'object') || Object.keys(value).length > 0);
export function draftDecision(review) {
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
    if (pr.owned !== true || pr.draft !== true) return ['hold', 'existing_pr_not_owned_draft'];
  } else if (review.publication_outcome === 'verified') return ['hold', 'verified_publication_missing_from_inventory'];
  if (['concrete_change', 'missing_from_fork', 'isolated_tests_passed'].some((key) => review[key] !== true)) return ['hold', 'concrete_isolated_change_not_proven'];
  if (typeof review.tested_head !== 'string' || !/^[0-9a-f]{40}(?![\s\S])/.test(review.tested_head)) return ['hold', 'exact_tested_head_required'];
  if (!Array.isArray(review.test_evidence) || !review.test_evidence.length || review.test_evidence.some((v) => typeof v !== 'string' || !v) || ['risks', 'rollout'].some((key) => typeof review[key] !== 'string' || !review[key])) return ['hold', 'tests_risks_rollout_required'];
  return matches.length ? ['update_existing_draft', 'owned_existing_draft'] : ['create_draft', 'tested_missing_change'];
}
export function report(review) {
  if (typeof review?.finding_id !== 'string' || !review.finding_id || review.finding_id.length > 1024) throw new Error('stable finding ID required');
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
    console.log(JSON.stringify(report(JSON.parse(input.toString('utf8')))));
  } catch (error) {
    console.log(JSON.stringify({ status: 'hold_policy_input_unavailable', error: error.message }));
    process.exitCode = 2;
  }
}
