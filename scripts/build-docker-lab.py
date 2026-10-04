#!/usr/bin/env python3
"""Build local lab images; baseline must be an archived pre-change working tree."""
import argparse
import subprocess,pathlib,json,concurrent.futures,time
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--baseline',type=pathlib.Path,required=True)
parser.add_argument('--output',type=pathlib.Path,default=pathlib.Path('/tmp/fleetrelay-docker-lab'))
args=parser.parse_args()
out=args.output.resolve();out.mkdir(parents=True,exist_ok=True)
root=pathlib.Path(__file__).resolve().parents[1]
baseline=args.baseline.resolve()
if not (baseline/'cmd/dispatcher/outbox.go').is_file():
    parser.error('baseline must contain the pre-optimization Dispatcher working tree')
def build(service,variant):
 path=out/('build-'+service+'-'+variant+'.log');context=baseline if variant=='baseline' else root;image='fleetrelay-lab-'+service+':'+variant;start=time.monotonic()
 cmd=['docker','build','--progress=plain','-f',str(context/'deploy/docker/go-service.Dockerfile'),'--build-arg','SERVICE='+service,'-t',image,str(context)]
 with path.open('w') as f:r=subprocess.run(cmd,stdout=f,stderr=subprocess.STDOUT)
 result={'service':service,'variant':variant,'image':image,'command':cmd,'exit_code':r.returncode,'elapsed_seconds':time.monotonic()-start,'log':str(path)}
 if not r.returncode:result['inspect']=json.loads(subprocess.check_output(['docker','image','inspect',image]))[0]
 else:print(path.read_text()[-3500:],flush=True)
 print(service,variant,'exit',r.returncode,flush=True);return result
jobs=[('dispatcher','baseline')]+[(s,'candidate') for s in ['control-api','stream-gateway','dispatcher','device-sim']]
with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:results=list(pool.map(lambda job:build(*job),jobs))
(out/'image-builds.json').write_text(json.dumps(results,indent=2));assert all(r['exit_code']==0 for r in results)
