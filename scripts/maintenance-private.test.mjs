import assert from 'node:assert/strict';
import { existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const ignore = readFileSync(new URL('../.gitignore', import.meta.url), 'utf8');
const runbook = readFileSync(new URL('../docs/maintenance.md', import.meta.url), 'utf8');
function git(root, args, expected = 0) {
  const result = spawnSync('git', args, { cwd: root, encoding: 'utf8' });
  assert.equal(result.status, expected, result.stderr || result.error?.message);
  return result.stdout;
}
function records(root) {
  mkdirSync(join(root, '.maintenance-private/history'), { recursive: true });
  writeFileSync(join(root, '.maintenance-private/current.md'), 'synthetic finding only\n');
  writeFileSync(join(root, '.maintenance-private/history/dummy.md'), 'synthetic history only\n');
  mkdirSync(join(root, '.maintenance-private/runs'), { recursive: true });
}
function fixture(t, initialIgnore = ignore, createRecords = true) {
  const root = mkdtempSync(join(tmpdir(), 'wacli-private-ignore-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  git(root, ['init', '-q']);
  writeFileSync(join(root, '.gitignore'), initialIgnore);
  if (createRecords) records(root);
  return root;
}
// Exercise the runbook's Git prerequisites, not a runtime copy/install helper.
function copyPrerequisites(root) {
  const tracked = git(root, ['ls-files', '-z', '--', '.maintenance-private/']);
  const targets = ['.maintenance-private/current.md', '.maintenance-private/history/dummy.md'];
  const excluded = targets.every((path) => spawnSync('git',
    ['check-ignore', '--quiet', '--no-index', '--', path], { cwd: root }).status === 0);
  return { trackedEmpty: tracked.length === 0, excluded, allowed: tracked.length === 0 && excluded };
}

test('root private records are effectively ignored and ordinary staging excludes them', (t) => {
  const root = fixture(t, ignore, false);
  assert.deepEqual(copyPrerequisites(root), { trackedEmpty: true, excluded: true, allowed: true });
  records(root);
  writeFileSync(join(root, 'public.txt'), 'synthetic public file\n');
  git(root, ['add', '--all']);
  assert.equal(git(root, ['ls-files', '--', '.maintenance-private/']), '');
  assert.match(git(root, ['ls-files']), /^public\.txt$/m);
});

test('the private ignore rule is anchored to the repository root', (t) => {
  const root = fixture(t);
  mkdirSync(join(root, 'nested/.maintenance-private'), { recursive: true });
  writeFileSync(join(root, 'nested/.maintenance-private/dummy.md'), 'synthetic nested file\n');
  git(root, ['check-ignore', '--quiet', '--no-index', '--', 'nested/.maintenance-private/dummy.md'], 1);
  git(root, ['add', '--all']);
  assert.match(git(root, ['ls-files']), /^nested\/\.maintenance-private\/dummy\.md$/m);
});

test('already tracked private content fails the prerequisite even when an ignore matches', (t) => {
  const root = fixture(t, '');
  git(root, ['add', '.maintenance-private/current.md']);
  git(root, ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
    'commit', '-qm', 'synthetic tracked-file fixture']);
  writeFileSync(join(root, '.gitignore'), ignore);
  assert.deepEqual(copyPrerequisites(root), { trackedEmpty: false, excluded: true, allowed: false });
  assert.equal(git(root, ['ls-files', '--', '.maintenance-private/']), '.maintenance-private/current.md\n');
});

test('an overridden ignore fails the prerequisite before any private copy', (t) => {
  const root = fixture(t, ignore + '\n!/.maintenance-private/\n!/.maintenance-private/**\n');
  assert.deepEqual(copyPrerequisites(root), { trackedEmpty: true, excluded: false, allowed: false });
});

test('Docker build contexts retain the private-directory exclusion', () => {
  const dockerIgnore = readFileSync(new URL('../.dockerignore', import.meta.url), 'utf8');
  assert.ok(dockerIgnore.split(/\r?\n/).map((line) => line.trim()).includes('.maintenance-private/'));
});

test('one-line entry resolves the maintained file and exact relay without a private config', (t) => {
  const root = fixture(t);
  mkdirSync(join(root, 'docs'));
  writeFileSync(join(root, 'docs/maintenance.md'), runbook);
  const entries = [...runbook.matchAll(/^> (.+)$/gm)];
  assert.equal(entries.length, 1);
  assert.equal(entries[0][1], 'Read and follow `<checkout>/docs/maintenance.md`');
  const prompt = entries[0][1].replaceAll('`', '').replace('<checkout>', root);
  assert.equal(prompt.split('\n').length, 1);
  const loaded = readFileSync(prompt.slice('Read and follow '.length), 'utf8');
  assert.equal(loaded, runbook);
  assert.equal(existsSync(join(root, '.maintenance-private/bindings.md')), false);
  assert.doesNotMatch(loaded, /bindings\.md/);
  const expected = 'After maintenance completes, whatever the result, send its full result once to the Codex chat ‘Relay maintenance test’ (thread 6af404ad-d96c-8191-8113-d53ab92be012). If the send is blocked or its outcome is uncertain, report that here and do not retry blindly.';
  assert.equal(loaded.split(expected).length - 1, 1);
});

test('private record locations are fixed relative to the document checkout', (t) => {
  const root = fixture(t);
  for (const relative of ['.maintenance-private/current.md', '.maintenance-private/history/', '.maintenance-private/runs/']) {
    assert.ok(runbook.includes('`' + relative + '`'));
    assert.equal(existsSync(join(root, relative)), true);
  }
  assert.doesNotMatch(runbook, /\/Users\//);
  assert.match(runbook, /DRAFT PR only in\n`artemgetmann\/wacli`/);
});
