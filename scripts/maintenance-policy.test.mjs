import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { draftDecision, report } from './maintenance-policy.mjs';
const cases = JSON.parse(readFileSync(new URL('./testdata/maintenance-policy-v1.json', import.meta.url)));
// Golden results captured from the pre-extraction Python policy, not a second implementation.
for (const value of cases) test(`legacy policy equivalence: ${value.name}`, () => {
  if (value.error) assert.throws(() => draftDecision(value.review));
  else assert.deepEqual(draftDecision(value.review), value.expected);
});
test('neutral versioned report binds stable ID and exact producer', () => {
  const result = report(cases[0].review);
  assert.equal(result.finding_id, cases[0].review.finding_id);
  assert.equal(result.contract_version, 1);
  assert.equal(result.policy_version, '1.0.0');
  assert.match(result.producer_sha256, /^[0-9a-f]{64}$/);
  assert.equal('item' in result, false);
  assert.equal('ledger' in result, false);
});
test('CLI emits report and fails closed on invalid input', () => {
  const tool = new URL('./maintenance-policy.mjs', import.meta.url);
  const ok = spawnSync(process.execPath, [tool.pathname], { input: JSON.stringify(cases[0].review), encoding: 'utf8' });
  assert.equal(ok.status, 0);
  assert.deepEqual(JSON.parse(ok.stdout), report(cases[0].review));
  const bad = spawnSync(process.execPath, [tool.pathname], { input: '{', encoding: 'utf8' });
  assert.equal(bad.status, 2);
  assert.equal(JSON.parse(bad.stdout).status, 'hold_policy_input_unavailable');
});
test('stdin reader bounds actual reads and buffers to limit+1', async () => {
  const { readReviewInput, MAX_REVIEW_BYTES } = await import('./maintenance-policy.mjs');
  let consumed = 0;
  assert.throws(() => readReviewInput(0, (_fd, buffer, offset, length) => {
    assert.equal(buffer.length, MAX_REVIEW_BYTES + 1);
    const count = Math.min(4096, length);
    buffer.fill(32, offset, offset + count);
    consumed += count;
    return count; // An endless writer: reader must stop without waiting for EOF.
  }), /review budget exceeded/);
  assert.equal(consumed, MAX_REVIEW_BYTES + 1);
});
test('CLI accepts exact-limit JSON and rejects oversized input', () => {
  const tool = new URL('./maintenance-policy.mjs', import.meta.url);
  const json = JSON.stringify(cases[0].review);
  const exact = json + ' '.repeat(262144 - Buffer.byteLength(json));
  const run = (input) => spawnSync(process.execPath, [tool.pathname], { input, encoding: 'utf8' });
  assert.equal(run(exact).status, 0);
  const over = run(exact + ' ');
  assert.equal(over.status, 2);
  assert.equal(JSON.parse(over.stdout).error, 'review budget exceeded');
});
