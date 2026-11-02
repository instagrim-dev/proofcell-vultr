"""Qualify both repo-authored branches without GitHub credentials."""
import hashlib,json,subprocess,tempfile
from pathlib import Path
root=Path(__file__).resolve().parent.parent/'examples/subscription'
cfg=json.loads((root/'.trajectory.json').read_text())
for choice in cfg['choices']:
 with tempfile.TemporaryDirectory() as tmp:
  out=Path(tmp)
  for change in choice['changes']:
   assert hashlib.sha256((root/change['path']).read_bytes()).hexdigest()==change['before_sha256']
   (out/change['path']).write_text(change['content'])
  for check in choice['checks']:
   assert json.loads((out/check['path']).read_text())[check['key']]==check['equals']
  expected='true' if choice['id']=='keep' else 'false'
  (out/'go.mod').write_text('module fixture\ngo 1.22\n')
  (out/'access_test.go').write_text('package subscription\nimport "testing"\nfunc TestChoice(t *testing.T){if HasAccess(30,12)!='+expected+'{t.Fatal("wrong selected behavior")};if HasAccess(30,30){t.Fatal("access past boundary")};if HasAccess(35,34)!='+expected+'{t.Fatal("lost paid-through value")}}\n')
  subprocess.run(['go','test','./...'],cwd=out,check=True)
  print(choice['id']+': actual code and declared JSON checks qualified')
