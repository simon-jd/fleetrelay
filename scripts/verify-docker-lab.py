#!/usr/bin/env python3
"""Local-only Docker deployment/fault and paired latency verification.

Uses the explicitly named disposable tests/docker/compose.yml project. Build the
candidate service images and baseline Dispatcher first. No cloud credentials,
external endpoints or production DSNs are accepted. Raw output defaults to /tmp.
"""
import argparse
import base64
import concurrent.futures
import datetime as dt
import hashlib
import json
import math
import os
from pathlib import Path
import statistics
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
COMPOSE = ['docker', 'compose', '-p', 'fleetrelay-evidence-lab', '-f', str(ROOT / 'tests/docker/compose.yml')]
TOKEN = 'lab-operator-only'
DEVICE_IDS = [f'lab-device-{n}' for n in range(1, 5)]
ENV = dict(os.environ, LAB_TAG='candidate', DISPATCHER_IMAGE='fleetrelay-lab-dispatcher:candidate')


def shell(cmd, binary=False, **kwargs):
    return subprocess.check_output(cmd, cwd=ROOT, env=ENV, timeout=120, text=not binary, **kwargs)


def compose(*args):
    return shell(COMPOSE + list(args))


def endpoint(service, port):
    return 'http://' + compose('port', service, str(port)).strip()


def http(base, path, payload=None, auth=TOKEN, timeout=3):
    headers = {'Content-Type': 'application/json'}
    if auth:
        headers['Authorization'] = 'Bearer ' + auth
    req = urllib.request.Request(base + path, data=None if payload is None else json.dumps(payload).encode(), headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            body = response.read()
            return response.status, json.loads(body) if body else None
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def eventually(label, fn, seconds=60):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        try:
            value = fn()
            if value:
                return value
        except (OSError, ValueError, subprocess.CalledProcessError) as e:
            last = str(e)
        time.sleep(.1)
    raise AssertionError(f'{label} timed out: {last}')


def sql(query, database='fleetrelay'):
    return compose('exec', '-T', 'postgres', 'psql', '-X', '-U', 'fleetrelay', '-d', database, '-At', '--set', 'ON_ERROR_STOP=1', '-c', query).strip()


def uid(value):
    return str(uuid.UUID(value))


def online(api, device_ids=DEVICE_IDS):
    code, result = http(api, '/v1/devices')
    return code == 200 and set(device_ids) <= {d['device_id'] for d in result['devices'] if d['state'] == 'ONLINE'}


def lease(api, device):
    code, result = http(api, '/v1/leases/acquire', {'device_id': device})
    assert code in (200, 201), (code, result)
    # Controlled fixture only: keep lease expiry out of the latency/fault
    # experiment. This is recorded; the public API default remains 30 seconds.
    sql(f"UPDATE control_leases SET expires_at=now()+interval '1 hour' WHERE lease_id='{uid(result['lease_id'])}'")
    return result['lease_id']


def submit(api, device, lease_id, policy='REQUIRE_ONLINE', key=None):
    request = {'device_id': device, 'lease_id': lease_id, 'idempotency_key': key or str(uuid.uuid4()), 'command_type': 'PAUSE_TASK', 'payload': {'fixture': 'docker-lab'}, 'delivery_policy': policy, 'expires_at': (dt.datetime.now(dt.timezone.utc) + dt.timedelta(minutes=2)).isoformat()}
    start = time.perf_counter()
    code, result = http(api, '/v1/commands', request)
    assert 200 <= code < 300, (code, result)
    return result['command_id'], start, request


def completed(api, cid, seconds=60):
    last = None
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        code, last = http(api, '/v1/commands/' + cid)
        if code == 200 and last['status'] == 'SUCCEEDED':
            return last
        if code == 200 and last['status'] in ['FAILED', 'EXPIRED', 'CANCELLED', 'ACK_TIMEOUT']:
            raise AssertionError(f'command did not succeed: {last}')
        time.sleep(.02)
    raise AssertionError(f'command timed out: {last}')


def journal(number):
    return json.loads(compose('exec', '-T', 'device-' + str(number), 'cat', f'/var/lib/fleetrelay/device-{number}.json'))


def container(service):
    return compose('ps', '-aq', service).strip()


def inspect(service):
    return json.loads(shell(['docker', 'inspect', container(service)]))[0]


def prometheus(base, expression):
    code, result = http(base, '/api/v1/query?query=' + urllib.parse.quote(expression), auth=None)
    assert code == 200 and result['status'] == 'success', (code, result)
    return result['data']['result']


def inventory(database='fleetrelay'):
    result = {}
    for table in ['devices', 'device_sessions', 'control_leases', 'commands', 'command_attempts', 'audit_records', 'outbox']:
        result[table] = sql(f"SELECT json_build_object('rows',count(*),'md5',md5(COALESCE(string_agg(row_to_json(t)::text,',' ORDER BY row_to_json(t)::text),''))) FROM {table} t", database)
    return result


def reliability(out, api, prom):
    records = []
    def record(name, data):
        records.append({'scenario': name, 'mode': 'local_docker_and_simulated_device', 'status': 'passed', **data})
        (out / 'reliability.json').write_text(json.dumps(records, indent=2) + '\n')
        print('PASS', name, flush=True)

    eventually('online devices', lambda: online(api))
    cid, _, payload = submit(api, DEVICE_IDS[0], lease(api, DEVICE_IDS[0]))
    code, duplicate = http(api, '/v1/commands', payload)
    assert 200 <= code < 300 and duplicate['command_id'] == cid
    completed(api, cid)
    assert cid in journal(1)['executed']
    assert sql(f"SELECT count(*) FROM command_attempts WHERE command_id='{uid(cid)}'") == '1'
    runtimes = {s: inspect(s) for s in ['control-api', 'dispatcher', 'gateway-a', 'gateway-b', 'device-1']}
    for name, runtime in runtimes.items():
        assert runtime['HostConfig']['ReadonlyRootfs'] and runtime['HostConfig']['Memory'] == 256 * 1024 * 1024
        assert runtime['Config']['User'] == 'fleetrelay' and runtime['HostConfig']['NanoCpus'] == 1000000000
        assert compose('exec', '-T', name, 'id', '-u').strip() != '0'
    record('container_deployment_and_idempotency', {'command_id': cid, 'duplicate_submission_same_id': True, 'attempts': 1, 'runtime': {k: {'image_id': v['Image'], 'user': v['Config']['User'], 'memory_limit_bytes': v['HostConfig']['Memory'], 'cpu_limit_nanocpus': v['HostConfig']['NanoCpus'], 'read_only_root': v['HostConfig']['ReadonlyRootfs']} for k, v in runtimes.items()}})

    compose('--profile', 'fault', 'up', '-d', 'device-crash')
    eventually('crash simulator online', lambda: online(api, ['lab-device-crash']))
    before_restarts = inspect('device-crash')['RestartCount']
    try: before_effects = journal('crash')['simulated_effects']
    except subprocess.CalledProcessError: before_effects = 0
    cid, _, _ = submit(api, 'lab-device-crash', lease(api, 'lab-device-crash'))
    completed(api, cid)
    state = journal('crash')
    after_restarts = inspect('device-crash')['RestartCount']
    assert after_restarts > before_restarts and state['simulated_effects'] == before_effects + 1 and cid in state['executed']
    (out / 'crash-journal.json').write_text(json.dumps(state, indent=2))
    (out / 'crash-device.log').write_text(compose('logs', '--no-color', 'device-crash'))
    record('simulator_crash_after_persist_before_ack', {'command_id': cid, 'process_restarts_delta': after_restarts-before_restarts, 'simulated_effects_delta': 1, 'attempts': int(sql(f"SELECT count(*) FROM command_attempts WHERE command_id='{uid(cid)}'")), 'boundary': 'Counter and result share one fsynced snapshot; this does not model atomic physical actuator effects.'})

    # Hold accepted work while the old Gateway actually receives SIGKILL.
    compose('pause', 'dispatcher')
    cid, _, _ = submit(api, DEVICE_IDS[0], lease(api, DEVICE_IDS[0]))
    old_gateway = sql("SELECT gateway_id FROM device_sessions WHERE device_id='lab-device-1' ORDER BY session_generation DESC LIMIT 1")
    shell(['docker', 'update', '--restart=no', container(old_gateway)])
    start = time.perf_counter()
    shell(['docker', 'kill', '--signal=KILL', container(old_gateway)])
    try:
        new_gateway = eventually('replacement Gateway session', lambda: (g if (g := sql("SELECT gateway_id FROM device_sessions WHERE device_id='lab-device-1' ORDER BY session_generation DESC LIMIT 1")) != old_gateway else None))
        compose('unpause', 'dispatcher')
        completed(api, cid)
        elapsed = time.perf_counter()-start
        assert cid in journal(1)['executed']
        record('gateway_sigkill_accepted_command_recovery', {'command_id': cid, 'from': old_gateway, 'to': new_gateway, 'recovery_seconds': elapsed, 'old_exit_code': inspect(old_gateway)['State']['ExitCode'], 'boundary': 'Accepted command held before scheduling; not an in-flight physical execution test.'})
    finally:
        if inspect('dispatcher')['State']['Paused']:
            compose('unpause', 'dispatcher')
        compose('up', '-d', '--no-deps', '--force-recreate', old_gateway)

    eventually('devices after gateway recovery', lambda: online(api))
    lid = lease(api, DEVICE_IDS[1])
    compose('pause', 'redis')
    try:
        cid, _, _ = submit(api, DEVICE_IDS[1], lid, policy='QUEUE_UNTIL')
        eventually('failed publication persisted', lambda: sql(f"SELECT EXISTS(SELECT 1 FROM outbox WHERE aggregate_id='{uid(cid)}' AND publication_failures>0)") == 't', seconds=15)
        pending = sql(f"SELECT status FROM commands WHERE command_id='{uid(cid)}'")
        assert pending == 'PENDING_TRANSPORT'
        start = time.perf_counter()
    finally: compose('unpause', 'redis')
    completed(api, cid)
    record('redis_pause_and_recovery', {'command_id': cid, 'outage_status': pending, 'recovery_seconds_after_resume': time.perf_counter()-start, 'same_command_succeeded': True})

    eventually('devices before database kill', lambda: online(api))
    compose('pause', 'dispatcher')
    cid, _, _ = submit(api, DEVICE_IDS[2], lease(api, DEVICE_IDS[2]))
    start = time.perf_counter()
    shell(['docker', 'kill', '--signal=KILL', container('postgres')])
    try:
        try: readiness = http(api, '/readyz', auth=None, timeout=2)[0]
        except OSError: readiness = 'client_timeout'
        assert readiness != 200
    finally: compose('start', 'postgres')
    eventually('PostgreSQL WAL recovery', lambda: sql('SELECT 1') == '1')
    assert sql(f"SELECT status FROM commands WHERE command_id='{uid(cid)}'") == 'PENDING_TRANSPORT'
    compose('unpause', 'dispatcher')
    completed(api, cid)
    record('postgres_sigkill_committed_command_recovery', {'command_id': cid, 'readiness_during_outage': readiness, 'recovery_seconds': time.perf_counter()-start, 'boundary': 'Single PostgreSQL container WAL recovery on its retained volume; not managed failover, RPO certification or regional DR.'})

    eventually('Prometheus all application targets up', lambda: len(prometheus(prom, 'up{job=~"control-api|dispatcher|stream-gateway"} == 1')) == 4)
    metrics = prometheus(prom, 'fleetrelay_outbox_pending{job="dispatcher"}')
    assert metrics
    grafana = endpoint('grafana', 3000)
    def grafana_dashboard():
        req = urllib.request.Request(grafana+'/api/dashboards/uid/fleetrelay-command-delivery', headers={'Authorization': 'Basic '+base64.b64encode(b'admin:lab-grafana-only').decode()})
        with urllib.request.urlopen(req, timeout=3) as r: return json.load(r)
    dashboard = eventually('provisioned Grafana dashboard', grafana_dashboard)
    assert any('fleetrelay_outbox_pending' in t.get('expr','') for p in dashboard['dashboard']['panels'] for t in p.get('targets',[]))
    (out / 'grafana-dashboard.json').write_text(json.dumps(dashboard, indent=2))
    record('live_prometheus_and_grafana_provisioning', {'application_targets_up': 4, 'outbox_series': metrics, 'grafana_dashboard_uid': dashboard['dashboard']['uid'], 'boundary': 'HTTP API provisioning and real Prometheus samples; no visual panel rendering assertion.'})

    # The down-alert wait also provides a quiet backup window.
    eventually('drained backlog', lambda: sql('SELECT count(*) FROM outbox WHERE published_at IS NULL') == '0')
    compose('stop', 'dispatcher')
    try:
        before = inventory()
        dump = shell(COMPOSE+['exec','-T','postgres','pg_dump','-U','fleetrelay','-d','fleetrelay','-Fc','--no-owner','--no-acl'], binary=True)
        (out/'database.dump').write_bytes(dump)
        restore_db = 'restore_' + uuid.uuid4().hex[:12]
        sql('CREATE DATABASE '+restore_db)
        subprocess.run(COMPOSE+['exec','-T','postgres','pg_restore','-U','fleetrelay','-d',restore_db,'--exit-on-error','--no-owner','--no-acl'], cwd=ROOT, env=ENV, input=dump, check=True, timeout=60)
        after = inventory(restore_db)
        assert before == after
        record('logical_backup_restore', {'database': restore_db, 'dump_sha256': hashlib.sha256(dump).hexdigest(), 'before': before, 'after': after, 'boundary': 'Logical backup into another database in the same local PostgreSQL instance; no WAL archive/PITR or independent-region restore.'})
        alerts = eventually('real Prometheus down alert firing', lambda: prometheus(prom, 'ALERTS{alertname="FleetRelayTargetDown",job="dispatcher",alertstate="firing"}'), seconds=50)
        record('live_dispatcher_down_alert', {'firing_samples': alerts})
    finally: compose('up', '-d', '--no-deps', 'dispatcher')
    eventually('down alert resolved', lambda: not prometheus(prom, 'ALERTS{alertname="FleetRelayTargetDown",job="dispatcher",alertstate="firing"}'))
    return records


def percentile(values, p):
    return sorted(values)[max(0, math.ceil(len(values)*p)-1)]


def workload(api, count):
    leases = {device: lease(api, device) for device in DEVICE_IDS}
    def worker(device):
        rows = []
        for _ in range(count):
            cid, start, _ = submit(api, device, leases[device])
            completed(api, cid)
            rows.append({'device_id':device, 'command_id':cid, 'latency_seconds':time.perf_counter()-start, 'status':'SUCCEEDED'})
        return rows
    started = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        rows = [r for group in pool.map(worker, DEVICE_IDS) for r in group]
    elapsed = time.perf_counter()-started
    latencies = [r['latency_seconds'] for r in rows]
    return {'requests':len(rows), 'failures':0, 'duration_seconds':elapsed, 'throughput_commands_per_second':len(rows)/elapsed, 'median_seconds':statistics.median(latencies), 'p95_seconds':percentile(latencies,.95), 'p99_seconds':percentile(latencies,.99), 'min_seconds':min(latencies), 'max_seconds':max(latencies), 'samples':rows}


def performance(out, api):
    results = []
    for pair in range(5):
        order = ['baseline','candidate'] if pair%2 == 0 else ['candidate','baseline']
        for variant in order:
            ENV['DISPATCHER_IMAGE']='fleetrelay-lab-dispatcher:'+variant
            compose('up','-d','--no-deps','--force-recreate','--wait','dispatcher')
            eventually('devices before load', lambda: online(api))
            workload(api,4)  # Excluded warm-up: four commands per simulated device.
            print('MEASURE',pair+1,variant,flush=True)
            result = workload(api,25)
            result.update(pair=pair+1,variant=variant,image_id=inspect('dispatcher')['Image'])
            result['container_stats_after']=compose('stats','--no-stream','--format','json')
            results.append(result)
            (out/'performance.json').write_text(json.dumps(results,indent=2)+'\n')
            print('RESULT',pair+1,variant,'p95_ms',round(result['p95_seconds']*1000,2),'commands_s',round(result['throughput_commands_per_second'],2),flush=True)
    ENV['DISPATCHER_IMAGE']='fleetrelay-lab-dispatcher:candidate'
    compose('up','-d','--no-deps','--force-recreate','--wait','dispatcher')
    medians={v:statistics.median([r['p95_seconds'] for r in results if r['variant']==v]) for v in ['baseline','candidate']}
    report={'mode':'local_docker_simulated_device_closed_loop','pairs':5,'concurrent_devices':4,'commands_per_device_per_run':25,'measured_commands_per_variant':500,'warmup_commands_per_device_per_run':4,'client_poll_interval_seconds':.02,'adapter_delay_ms':25,'lease_fixture':'Lease deadlines extended to one hour with SQL before each workload; command creation and result observation use REST.','p95_method':'Nearest-rank p95 per 100-command run; report median across five runs per variant.','median_run_p95_seconds':medians,'p95_reduction_percent':(medians['baseline']-medians['candidate'])/medians['baseline']*100,'formula':'(baseline median run p95 - candidate median run p95) / baseline median run p95 * 100','all_measured_commands_succeeded':all(r['requests']==100 and r['failures']==0 for r in results),'comparison_scope':'Only Dispatcher image changes; other services use the same candidate images, durable simulator journals and resource limits. Same local database grows across alternating AB/BA runs. No dedicated CPU isolation; not open-loop capacity, physical-device latency or production SLO.'}
    (out/'performance-summary.json').write_text(json.dumps(report,indent=2)+'\n')
    return report


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output',type=Path,default=Path('/tmp/fleetrelay-docker-lab'))
    parser.add_argument('--phase',choices=['all','reliability','performance'],default='all')
    args=parser.parse_args();out=args.output;out.mkdir(parents=True,exist_ok=True)
    try:
        compose('up','-d','--wait')
        api,prom=endpoint('control-api',8080),endpoint('prometheus',9090)
        (out/'endpoints.json').write_text(json.dumps({'api':api,'prometheus':prom,'postgres':compose('port','postgres','5432').strip(),'redis':compose('port','redis','6379').strip()},indent=2))
        if args.phase in ['all','reliability']:reliability(out,api,prom)
        if args.phase in ['all','performance']:performance(out,api)
    except Exception as e:
        (out/(args.phase+'-failure.json')).write_text(json.dumps({'type':type(e).__name__,'error':str(e),'at':dt.datetime.now(dt.timezone.utc).isoformat()},indent=2))
        raise
    finally:
        (out/(args.phase+'-compose.log')).write_text(compose('--profile','fault','logs','--no-color'))


if __name__=='__main__':main()
