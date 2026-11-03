"""Fake runtime credential mediation; upstream responses come from the real TLS fixture."""
import base64
import http.client
import importlib.util
import json
import os
import pathlib
import socket
import ssl
import sys
import time
import urllib.parse
import uuid

state, claims, broken, verb, *args = sys.argv[1:]
root = pathlib.Path(state) / 'docker-auth'
root.mkdir(exist_ok=True)
sys.dont_write_bytecode = True

if verb == 'create':
    # Adapter.Create places Kit references before every option.
    kits = args[:next((i for i, arg in enumerate(args) if arg.startswith('--')), len(args))]
    fixtures = [pathlib.Path(arg).name for arg in kits]
    optional = 'docker-auth-optional' in fixtures
    granted = 'docker-auth' in fixtures or optional
    if granted and 'com.docker.sandbox/docker-auth@1' not in claims:
        sys.exit(2)
    binding = json.loads(pathlib.Path(args[args.index('--docker-auth-config') + 1]).read_text())
    login = binding['bindings'].get('registry.kit-tck.test')
    if login:
        (root / 'cached-login.json').write_text(json.dumps(login))
    elif optional and broken == 'docker-auth-skipped-auth':
        binding['bindings']['registry.kit-tck.test'] = json.loads((root / 'cached-login.json').read_text())
    if granted and not login and broken != 'docker-auth-accepts-missing':
        if not optional:
            sys.exit(2)
        granted = False
    if broken == 'docker-auth-eager-no-network' and granted and 'docker-auth-network' not in fixtures:
        sys.exit(2)
    ident = uuid.uuid4().hex
    binding['probeEnv'] = dict(arg.split('=', 1) for i, arg in enumerate(args) if i and args[i-1] == '--env')
    data = {'binding': binding, 'granted': granted, 'optional': optional,
            'network': 'docker-auth-network' in fixtures or 'docker-auth-absent' in fixtures,
            'user': 'placeholder-user-' + ident, 'password': 'placeholder-password-' + ident,
            'tokens': {}, 'locations': {}}
    (root / (ident + '.json')).write_text(json.dumps(data))
    print(ident)
    sys.exit(0)

file = root / (args[0] + '.json')
if verb == 'rm':
    file.unlink(missing_ok=True)
    sys.exit(0)
if verb not in ('exec', 'selection'):
    sys.exit(0)
data = json.loads(file.read_text())
if verb == 'selection':
    skipped = []
    if data['optional'] and not data['granted']:
        source = {'kit': 'docker-auth-optional', 'path': 'capabilities[0]'}
        skipped = [{'path': 'capabilities[0]', 'source': source, 'members': ['capabilities[0]'],
                    'memberSources': [source], 'rejected': ['capabilities[0]']}]
    print(json.dumps({'selection': {'selected': [], 'skipped': skipped}, 'surface': {}}))
    sys.exit(0)

binding = data['binding']
login = binding['bindings'].get('registry.kit-tck.test', {})
spec = importlib.util.spec_from_file_location('auth_probe', pathlib.Path(__file__).parent / 'fixtures/workload/kit-tck-docker-auth.py')
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)

# The probe resolves credentials from a real Docker config written by the
# runtime, so its resolver code path is the one exercised.
guest = root / ('config-' + args[0])
guest.mkdir(exist_ok=True)
if data['granted']:
    pair = [login['username'], login['password']] if broken == 'docker-auth-login-leak' else [data['user'], data['password']]
    config = {'auths': {'registry.kit-tck.test': {'auth': base64.b64encode(':'.join(pair).encode()).decode()}}}
    if broken in ('docker-auth-identity-config', 'docker-auth-form-leak'):
        config = {'auths': {'registry.kit-tck.test': {'identitytoken': data['password']}}}
    if broken == 'docker-auth-all-bindings':
        other = binding['bindings']['cdn.kit-tck.test']
        config['auths']['cdn.kit-tck.test'] = {'auth': base64.b64encode((other['username'] + ':' + other['password']).encode()).decode()}
    if broken == 'docker-auth-encoded-login':
        config['auths']['unrelated.example.com'] = {'auth': base64.b64encode((login['username'] + ':' + login['password']).encode()).decode()}
    if broken == 'docker-auth-identitytoken-leak':
        config['auths']['registry.kit-tck.test']['identitytoken'] = login['password']
    (guest / 'config.json').write_text(json.dumps(config))
os.environ['DOCKER_CONFIG'] = str(guest)
create_args = binding.get('probeEnv', {})
os.environ.update(create_args)


class RoutedHTTPS(http.client.HTTPSConnection):
    def connect(self):
        address, port = binding['routes'][self.host].rsplit(':', 1)
        raw = socket.create_connection((address, int(port)), timeout=10)
        self.sock = ssl.create_default_context(cafile=binding['trust']).wrap_socket(raw, server_hostname=self.host)


def origin(url, authorization, method, body=None):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme == 'https':
        connection = RoutedHTTPS(parsed.hostname, timeout=10)
    else:
        address, port = binding['httpRoutes'][parsed.hostname].rsplit(':', 1)
        connection = http.client.HTTPConnection(address, int(port), timeout=10)
    try:
        connection.request(method, parsed.path + ('?' + parsed.query if parsed.query else ''),
                           body=body, headers={'Host': parsed.hostname, 'Authorization': authorization,
                                              **({'Content-Type': 'application/x-www-form-urlencoded'} if body is not None else {})})
        response = connection.getresponse()
        return response.status, probe.header_values(response.getheaders()), response.read().decode('utf-8', 'backslashreplace')
    finally:
        connection.close()


def mediated(url, authorization='', method='GET', body=None):
    parsed = urllib.parse.urlsplit(url)
    host, path = parsed.hostname, parsed.path
    if body is not None:
        if broken == 'docker-auth-form-leak' and host == 'cdn.kit-tck.test':
            leaked = urllib.parse.urlencode({'refresh_token': login['password']}).encode()
            return origin(url, '', 'POST', leaked)
        form = urllib.parse.parse_qs(body.decode())
        if form.get('refresh_token') != [data['password']]:
            return 401, {}, ''
        authorization = 'Basic ' + base64.b64encode((data['user'] + ':' + data['password']).encode()).decode()
        method = 'GET'
    if path in ('/v2/', '/v2/replay') and not authorization and (broken in ('docker-auth-skipped-auth', 'docker-auth-absent-auth') and not data['granted'] or broken == 'docker-auth-host-discovery' and data['granted'] and data['network']):
        _, headers, _ = origin(url, '', method)
        challenge = headers['www-authenticate'][0].split(' ', 1)[1]
        params = probe.urllib.request.parse_keqv_list(probe.urllib.request.parse_http_list(challenge))
        auth = 'Basic ' + base64.b64encode((login['username'] + ':' + login['password']).encode()).decode()
        status, _, body = origin(params['realm'] + '&service=' + params['service'], auth, 'GET')
        if status != 200:
            return 502, {}, ''
        return origin(url, 'Bearer ' + json.loads(body)['token'], method)
    if (parsed.scheme != 'https' and broken != 'docker-auth-plaintext') or (not data['network'] and broken != 'docker-auth-egress' and not (authorization and broken == 'docker-auth-substitution-egress')):
        return 0, {}, ''
    if host == 'cdn.kit-tck.test' and path == '/blocked' and broken != 'docker-auth-redirect-policy':
        return 403, {}, ''
    if path.startswith('/v2/held/'):
        entry = urllib.parse.parse_qs(parsed.query).get('target', [None])[0] if broken == 'docker-auth-wrapped-location' else data['locations'].get(path)
        return mediated(entry, authorization, method) if entry else (502, {}, '')
    if authorization == 'Basic ' + base64.b64encode((data['user'] + ':' + data['password']).encode()).decode():
        # The login-scope mutation relaxes only the host bound; path, service
        # and challenge state stay validated.
        admitted = (host == 'auth.kit-tck.test' or broken == 'docker-auth-login-scope') and path in ('/token', '/token/replay') and \
            urllib.parse.parse_qs(parsed.query).get('service') == ['kit-tck-registry']
        if admitted:
            user = data['user'] if broken == 'docker-auth-login-scope' and host != 'auth.kit-tck.test' else login['username']
            authorization = 'Basic ' + base64.b64encode((user + ':' + login['password']).encode()).decode()
    placeholder = ''
    if authorization.startswith('Bearer '):
        placeholder = authorization[7:]
        token = data['tokens'].get(placeholder)
        if token is None and broken == 'docker-auth-unknown-bearer' and data['tokens']:
            token = next(iter(data['tokens'].values()))
        if token is None and broken == 'docker-auth-reuses-run':
            for candidate in root.glob('global-*.json'):
                token = json.loads(candidate.read_text()).get(placeholder, token)
        # Substitution is confined to the admitted HTTPS registry API; the
        # placeholder travels opaquely everywhere else, including the CDN.
        if token and (host == 'registry.kit-tck.test' and path.startswith('/v2/') or broken == 'docker-auth-bearer-scope'):
            if time.time() >= token['expires'] and broken == 'docker-auth-expiry':
                return 200, {}, data['cached-content']
            if time.time() < token['expires'] or broken == 'docker-auth-stale-rejected':
                authorization = 'Bearer ' + token['secret']
            else:
                return 401, {}, ''  # expired authorization fails closed
    try:
        status, headers, body = origin(url, authorization, method)
    except (OSError, http.client.HTTPException):
        return 0, {}, ''
    if broken == 'docker-auth-unknown-bearer' and placeholder == 'kit-tck-unknown-placeholder':
        return 401, {}, ''
    if host == 'auth.kit-tck.test' and path in ('/token', '/token/replay') and data['granted']:
        if broken == 'docker-auth-token-leak':
            return status, headers, body
        if status != 200:
            # Unsupported or failed exchanges fail closed; upstream error
            # details stay host-side.
            return 502, {}, ''
        placeholder = 'bearer-placeholder-' + uuid.uuid4().hex
        payload = json.loads(body)
        data['tokens'][placeholder] = {'secret': payload['token'], 'expires': time.time() + payload['expires_in']}
        (root / ('global-' + args[0] + '.json')).write_text(json.dumps(data['tokens']))
        fields = {'token': placeholder, 'expires_in': payload['expires_in']}
        if broken == 'docker-auth-extra-field-leak':
            fields['access_token'] = payload['token']
        headers = {'authorization': ['Basic ' + base64.b64encode((login['username'] + ':' + login['password']).encode()).decode(),
                                      'Bearer ' + payload['token']]} if broken == 'docker-auth-header-leak' else {}
        return 200, headers, json.dumps(fields)
    location = next(iter(headers.get('location', [])), '')
    if location and broken != 'docker-auth-location-leak' and not (broken == 'docker-auth-upload-leak' and path.startswith('/v2/uploads')):
        held = '/v2/held/' + uuid.uuid4().hex
        data['locations'][held] = urllib.parse.urljoin(url, location)
        if broken == 'docker-auth-wrapped-location':
            held += '?' + urllib.parse.urlencode({'target': data['locations'][held]})
        original = urllib.parse.urljoin(url, location)
        headers = {'location': ['https://registry.kit-tck.test' + held]}
        if broken == 'docker-auth-content-location':
            headers['content-location'] = [original]
    return status, headers, body


probe.transport_request = lambda url, authorization='', method='GET', data=None: mediated(url, authorization, method, data)
probe.daemon_state = lambda: {'daemon': False, 'endpoint': False}
mode = args[3]
sys.argv = [sys.argv[0], *args[3:]]
try:
    if mode == 'ambient' and broken == 'docker-auth-daemon':
        result = probe.observe(mode)
        if data['granted']:
            result.update(daemon=True, endpoint=True)
    elif mode == 'short' and broken == 'docker-auth-expiry':
        result = probe.observe(mode)
        response = probe.request('https://registry.kit-tck.test/v2/', 'Bearer ' + result)
        data['cached-content'] = response['body']
    elif mode == 'hooks':
        result = {phase: probe.observe('login') for phase in ('install', 'startup')}
        if broken == 'docker-auth-hook-leak':
            result['install']['pair'] = [login['password'], login['username']]
    else:
        result = probe.observe(mode)
    print(json.dumps(result))
except (OSError, ValueError, KeyError, IndexError, StopIteration):
    sys.exit(1)
finally:
    file.write_text(json.dumps(data))
