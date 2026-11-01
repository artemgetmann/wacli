#!/usr/bin/env python3
"""Regenerate the SDK companion diff from a hash-verified pristine module."""
import argparse,difflib,hashlib,json
from pathlib import Path
p=argparse.ArgumentParser();p.add_argument('--upstream-dir',type=Path,required=True);a=p.parse_args()
here=Path(__file__).resolve().parent;sdk=here.parent/'whatsmeow';meta=json.loads((here/'upstream.json').read_text())
actual={str(p.relative_to(a.upstream_dir)):hashlib.sha256(p.read_bytes()).hexdigest() for p in a.upstream_dir.rglob('*') if p.is_file()}
if actual!=meta['pristine_sha256']:raise SystemExit('Pristine module does not match the pinned manifest')
chunks=[];changes={}
for name in sorted(set(actual)|{str(p.relative_to(sdk)) for p in sdk.rglob('*') if p.is_file()}):
 before=(a.upstream_dir/name).read_bytes() if (a.upstream_dir/name).exists() else b''
 after=(sdk/name).read_bytes() if (sdk/name).exists() else b''
 if before==after:continue
 if '..' in Path(name).parts or Path(name).is_absolute():raise SystemExit('unsafe path')
 chunks.extend(difflib.unified_diff(before.decode().splitlines(keepends=True),after.decode().splitlines(keepends=True),fromfile='a/'+name if before else '/dev/null',tofile='b/'+name if after else '/dev/null'))
 changes[name]={'before':hashlib.sha256(before).hexdigest() if before else None,'after':hashlib.sha256(after).hexdigest() if after else None}
patch=''.join(chunks).encode();(here/'0001-durable-app-state-handoff.patch').write_bytes(patch)
(here/'changes.json').write_text(json.dumps({'patch_sha256':hashlib.sha256(patch).hexdigest(),'files':changes},indent=2)+'\n')
print(f'{len(changes)} changed SDK files; patch SHA256 {hashlib.sha256(patch).hexdigest()}')
