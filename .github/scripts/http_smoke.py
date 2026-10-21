"""Exercise the actual CLI HTTP server and both DBs; run only in CI/test environments."""
import json
import tempfile
import subprocess
import time
import urllib.request

base = 'http://127.0.0.1:8087'
def request(path, body=None):
    req = urllib.request.Request(base + path, data=None if body is None else json.dumps(body).encode(), headers={'Content-Type': 'application/json', 'X-Bench-Request': '1', 'Origin': base})
    with urllib.request.urlopen(req, timeout=10) as response:
        return response.read()

binary = tempfile.mktemp(prefix='bench-http-')
subprocess.run(['go', 'build', '-o', binary, './cmd/bench'], check=True)
proc = subprocess.Popen([binary, '-serve', '-listen', '127.0.0.1:8087', '-out', 'results/http'])
try:
    for _ in range(60):
        try:
            request('/api/jobs')
            break
        except OSError:
            if proc.poll() is not None:
                raise RuntimeError('server exited')
            time.sleep(0.5)
    else:
        raise RuntimeError('server did not start')
    assert '計測開始' in request('/').decode()
    config = dict(engines=['mongodb','postgresql'], sizes=[23], indexes=[0,1,5], repeats=1, batch_size=7, seed=42, journal=True, pg_fillfactor=80, operation_timeout_ns=30_000_000_000, cooldown_ns=0)
    job = json.loads(request('/api/jobs', config))
    for _ in range(180):
        current = next(j for j in json.loads(request('/api/jobs')) if j['id'] == job['id'])
        if current['state'] not in ('running','cancelling'):
            break
        time.sleep(0.5)
    assert current['state'] == 'completed', current
    assert current['done'] == current['total'] == 24, current
    report = json.loads(request('/results/' + job['id'] + '/results.json'))
    assert report['completed'] and {r['engine'] for r in report['results']} == {'mongodb','postgresql'}
    html = request('/results/' + job['id'] + '/report.html').decode()
    payload = json.loads(html.split('<script id="payload" type="application/json">')[1].split('</script>')[0])
    assert len(payload['summary']) == 24
    assert all(s['ratio'] == 1 for s in payload['summary'] if s['indexes'] == 0)
    config['cooldown_ns'] = 5_000_000_000
    job = json.loads(request('/api/jobs', config))
    request('/api/cancel', {})
    for _ in range(60):
        current = next(j for j in json.loads(request('/api/jobs')) if j['id'] == job['id'])
        if current['state'] not in ('running','cancelling'):
            break
        time.sleep(0.5)
    assert current['state'] == 'cancelled', current
    partial = json.loads(request('/results/' + job['id'] + '/results.json'))
    assert not partial['completed']
finally:
    proc.terminate()
    try:
        proc.wait(timeout=45)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()
