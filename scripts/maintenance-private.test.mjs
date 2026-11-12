import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const ignore = readFileSync(new URL('../.gitignore', import.meta.url), 'utf8');
function git(root, args, expected = 0) {
  const result = spawnSync('git', args, { cwd: root, encoding: 'utf8' });
  assert.equal(result.status, expected, result.stderr || result.error?.message);
  return result.stdout;
}
function records(root) {
  mkdirSync(join(root, '.maintenance-private/history'), { recursive: true });
  writeFileSync(join(root, '.maintenance-private/bindings.md'), 'synthetic binding only\n');
  writeFileSync(join(root, '.maintenance-private/history/dummy.md'), 'synthetic history only\n');
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
  const targets = ['.maintenance-private/bindings.md', '.maintenance-private/history/dummy.md'];
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
  git(root, ['add', '.maintenance-private/bindings.md']);
  git(root, ['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
    'commit', '-qm', 'synthetic tracked-file fixture']);
  writeFileSync(join(root, '.gitignore'), ignore);
  assert.deepEqual(copyPrerequisites(root), { trackedEmpty: false, excluded: true, allowed: false });
  assert.equal(git(root, ['ls-files', '--', '.maintenance-private/']), '.maintenance-private/bindings.md\n');
});

test('an overridden ignore fails the prerequisite before any private copy', (t) => {
  const root = fixture(t, ignore + '\n!/.maintenance-private/\n!/.maintenance-private/**\n');
  assert.deepEqual(copyPrerequisites(root), { trackedEmpty: true, excluded: false, allowed: false });
});
