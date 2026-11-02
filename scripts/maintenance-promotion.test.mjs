import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { draftDecision, promotionDecision, promotionReport } from './maintenance-policy.mjs';
const fixture = JSON.parse(readFileSync(new URL('./testdata/maintenance-promotion-v1.json', import.meta.url)));
const review = () => structuredClone(fixture);
const held = (r, reason) => assert.deepEqual(promotionDecision(r), ['hold', reason]);
test('complete exact-head evidence permits only the bound ready transition', () => {
  const r = review();
  assert.deepEqual(promotionDecision(r), ['mark_ready', 'exact_head_implementation_tests_review_complete']);
  const p = promotionReport(r);
  assert.equal(p.operation, 'promotion');
  assert.equal(p.finding_id, r.finding_id);
  assert.equal(p.pr_url, r.existing_prs[0].url);
  assert.equal(p.reviewed_head, r.tested_head);
  assert.equal(p.publication_performed, false);
  assert.equal('decision' in p, false); // Cannot masquerade as a draft policy_result.
  assert.equal(p.policy_version, '1.1.0');
});
test('verified already-ready is idempotent but does not relax draft editing', () => {
  const r = review(); r.existing_prs[0].draft = false;
  assert.deepEqual(promotionDecision(r), ['already_ready', 'verified_ready_no_transition']);
  assert.deepEqual(draftDecision(r), ['hold', 'existing_pr_not_owned_draft']);
  r.publication_outcome = 'unknown'; held(r, 'reconcile_publication_before_retry');
});
for (const [field, reason] of [
  ['implementation_complete', 'implementation_incomplete'],
  ['applicable_tests_passed', 'applicable_tests_incomplete'],
  ['independent_review_passed', 'independent_review_incomplete'],
  ['owner_overlap_checked', 'owner_overlap_not_verified'],
]) test(`missing, false or nonboolean ${field} holds`, () => {
  for (const value of [undefined, null, false, 'true', 1, []]) {
    const r = review(); r[field] = value; held(r, reason);
  }
});
test('review must be independent, evidenced and free of unresolved findings', () => {
  for (const changes of [
    { independent_review_evidence: [] }, { independent_review_evidence: [' '] },
    { independent_review_evidence: ['x'.repeat(2049)] },
  ]) held(Object.assign(review(), changes), 'independent_review_incomplete');
  for (const changes of [
    { independent_reviewer: ' FIXTURE:IMPLEMENTER ' }, { independent_reviewer: '' },
    { implementation_owner: null },
  ]) held(Object.assign(review(), changes), 'independent_reviewer_required');
  for (const value of [undefined, null, 'none', ['unresolved']]) {
    const r = review(); r.unresolved_findings = value; held(r, 'unresolved_review_findings');
  }
});
test('tested, independently reviewed and authoritative remote heads must be identical', () => {
  for (const head of ['b'.repeat(40), 'abc', null, 123]) {
    const r = review(); r.independent_review_head = head; held(r, 'promotion_heads_do_not_match');
    const remote = review(); remote.existing_prs[0].head_sha = head; held(remote, 'promotion_heads_do_not_match');
  }
  const r = review(); r.tested_head = 'b'.repeat(40); held(r, 'promotion_heads_do_not_match');
});
test('remote inventory must be evidenced, timezone-aware, fresh and not future-dated', () => {
  for (const observed of [undefined, null, '2026-11-02T14:59:00', '2026-11-02T15:00:01Z', '2026-11-02T14:54:59.999Z', '2026-02-30T14:59:00Z']) {
    const r = review(); r.pr_inventory_observed_at = observed; held(r, 'remote_inventory_not_fresh');
  }
  for (const invalid of ['2026-02-30T15:00:00Z', '2026-11-02T24:00:00Z', '2026-11-02T15:00:00Z\n', 123]) {
    const r = review(); r.decision_at = invalid; held(r, 'remote_inventory_not_fresh');
  }
  const r = review(); r.remote_inventory_evidence = []; held(r, 'remote_inventory_not_fresh');
  const boundary = review(); boundary.pr_inventory_observed_at = '2026-11-02T14:55:00Z';
  assert.equal(promotionDecision(boundary)[0], 'mark_ready');
});
test('inventory, equivalent intent, ownership and publication safeguards remain fail-closed', () => {
  const equivalent = review(); equivalent.existing_prs[0].finding_id = 'fixture:other'; equivalent.existing_prs[0].equivalent_intent = true;
  assert.equal(promotionDecision(equivalent)[0], 'mark_ready');
  for (const [change, reason] of [
    [{ repository: 'other/repo' }, 'wrong_publication_repository'],
    [{ publication_outcome: 'unknown' }, 'reconcile_publication_before_retry'],
    [{ pr_inventory_complete: 'yes' }, 'deduplication_inventory_incomplete'],
    [{ existing_prs: [] }, 'matching_pr_required_for_promotion'],
    [{ overlapping_owner: 'fixture:other' }, 'existing_owner_retains_work'],
    [{ ancestry_pending: true }, 'decision_or_ancestry_pending'],
    [{ isolated_tests_passed: false }, 'concrete_isolated_change_not_proven'],
    [{ test_evidence: [] }, 'tests_risks_rollout_required'],
  ]) held(Object.assign(review(), change), reason);
  for (const [change, reason] of [
    [{ state: 'closed' }, 'closed_pr_disposition_requires_reconciliation'],
    [{ owned: false }, 'existing_pr_not_owned_draft'],
    [{ draft: null }, 'existing_pr_not_owned_draft'],
    [{ remote_verified: 1 }, 'matching_pr_remote_state_unverified'],
    [{ url: 'https://example.test/pr' }, 'matching_pr_remote_state_unverified'],
  ]) { const r = review(); Object.assign(r.existing_prs[0], change); held(r, reason); }
  const multiple = review(); multiple.existing_prs.push(structuredClone(multiple.existing_prs[0])); held(multiple, 'multiple_matching_prs_reconcile');
  assert.throws(() => promotionDecision({ ...review(), existing_prs: null }));
  assert.throws(() => promotionDecision({ ...review(), existing_prs: Array(65).fill({}) }));
  assert.throws(() => promotionDecision({ ...review(), finding_id: '' }));
});
test('public CLI separates promotion reports, preserves legacy draft requests and rejects unknown operations', () => {
  const tool = new URL('./maintenance-policy.mjs', import.meta.url).pathname;
  const run = (input) => spawnSync(process.execPath, [tool], { input: JSON.stringify(input), encoding: 'utf8' });
  const good = run({ operation: 'promotion', review: review() });
  assert.equal(good.status, 0); assert.deepEqual(JSON.parse(good.stdout), promotionReport(review()));
  const incomplete = review(); incomplete.applicable_tests_passed = false;
  const hold = run({ operation: 'promotion', review: incomplete });
  assert.equal(hold.status, 0); assert.equal(JSON.parse(hold.stdout).promotion_decision, 'hold');
  const legacy = run(review()); assert.equal(legacy.status, 0); assert.equal(JSON.parse(legacy.stdout).decision, 'update_existing_draft');
  for (const input of [{ operation: 'unknown', review: review() }, { operation: 'promotion' }]) {
    const bad = run(input); assert.equal(bad.status, 2); assert.equal(JSON.parse(bad.stdout).status, 'hold_policy_input_unavailable');
  }
});
