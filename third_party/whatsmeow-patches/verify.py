#!/usr/bin/env python3
"""Verify pristine source, apply the review patch in a temporary directory,
then compare every reconstructed file to the carried SDK. No network access."""
import argparse,hashlib,json,shutil,subprocess,tempfile
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--upstream-dir',type=Path,required=True);a=p.parse_args()
here=Path(__file__).resolve().parent;sdk=here.parent/'whatsmeow';meta=json.loads((here/'upstream.json').read_text());changes=json.loads((here/'changes.json').read_text())
def hashes(root):return {str(p.relative_to(root)):hashlib.sha256(p.read_bytes()).hexdigest() for p in root.rglob('*') if p.is_file()}
if hashes(a.upstream_dir)!=meta['pristine_sha256']:raise SystemExit('Pristine module hash mismatch')
patch=here/'0001-durable-app-state-handoff.patch'
if hashlib.sha256(patch.read_bytes()).hexdigest()!=changes['patch_sha256']:raise SystemExit('Patch hash mismatch')
with tempfile.TemporaryDirectory(prefix='wacli-sdk-verify-') as tmp:
 root=Path(tmp)/'sdk';shutil.copytree(a.upstream_dir,root)
 for p in [root,*root.rglob('*')]:p.chmod(p.stat().st_mode|0o200)
 subprocess.run(['patch','--batch','--forward','-p1','-d',str(root),'-i',str(patch)],check=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
 if hashes(root)!=hashes(sdk):raise SystemExit('Carried SDK differs from pristine source plus review patch')
print('SDK provenance verified: exact pinned source plus one explicit patch')
