"""Fake runtime authentication boundary; issuer responses come from the real TLS fixture."""
import base64
import importlib.util
import json
import os
import pathlib
import shutil
import sys
import time
import urllib.parse
import uuid
import http.client
import socket
import ssl

state, claims, broken, verb, *args = sys.argv[1:]
root = pathlib.Path(state) / 'docker-auth'
root.mkdir(exist_ok=True)
cap = 'com.docker.sandbox/docker-auth@1'
# Opaque realm state whose encoding changes if a client re-encodes the query.
OPAQUE_REALM_STATE = 'state=a+b%20c'
sys.dont_write_bytecode = True

if verb == 'create':
    # Adapter.Create places Kit references before every option.
    kits = args[:next((i for i, arg in enumerate(args) if arg.startswith('--')), len(args))]
    fixtures = [pathlib.Path(arg).name for arg in kits]
    granted = 'docker-auth' in fixtures or 'docker-auth-optional' in fixtures
    if broken == 'docker-auth-without-grant' and 'docker-auth-absent' in fixtures:
        granted = True
    optional = 'docker-auth-optional' in fixtures
    if granted and cap not in claims:
        if not optional:
            sys.exit(2)
        granted = False
    if '--docker-auth-config' not in args:
        sys.exit(2)
    binding = json.loads(pathlib.Path(args[args.index('--docker-auth-config') + 1]).read_text())
    login = binding['bindings'].get('registry.kit-tck.test', {})
    available = bool(login.get('username') and login.get('password'))
    if granted and not available and broken != 'docker-auth-accepts-missing' and not (optional and broken == 'docker-auth-retains-skipped'):
        if not optional:
            sys.exit(2)
        granted = False
    if optional and broken == 'docker-auth-retains-skipped' and not available:
        granted = True
        original = root / 'available-login.json'
        login = json.loads(original.read_text())
        binding['bindings']['registry.kit-tck.test'] = login
    if available:
        (root / 'available-login.json').write_text(json.dumps(login))
    ident = uuid.uuid4().hex
    data = {'binding': binding, 'granted': granted,
            'network': 'docker-auth-network' in fixtures or 'docker-auth-absent' in fixtures,
            'user': 'placeholder-user-' + ident, 'password': 'placeholder-password-' + ident,
            'tokens': {}, 'locations': {}, 'saved': '', 'optional': optional, 'hooks': 'docker-auth-hooks' in fixtures}
    if broken == 'docker-auth-reuses-login' and not (root / 'old-login.json').exists():
        (root / 'old-login.json').write_text(json.dumps([data['user'], data['password']]))
    (root / (ident + '.json')).write_text(json.dumps(data))
    print(ident)
    sys.exit(0)

file = root / (args[0] + '.json')
guest = root / ('config-' + args[0])
if verb == 'rm':
    shutil.rmtree(guest, ignore_errors=True)
    if file.exists():
        file.unlink()
    sys.exit(0)
if verb in ('stop', 'start', 'recreate'):
    sys.exit(0)
if verb not in ('exec', 'selection'):
    sys.exit(1)
data = json.loads(file.read_text())
if verb == 'selection':
    skipped = []
    if data['optional'] and (not data['granted'] or broken == 'docker-auth-retains-skipped') and broken != 'docker-auth-missing-skip':
        source = {'kit': 'docker-auth-optional', 'path': 'capabilities[0]'}
        skipped = [{'path': 'capabilities[0]', 'source': source, 'members': ['capabilities[0]'], 'memberSources': [source], 'rejected': ['capabilities[0]']}]
    if broken in ('docker-auth-extra-workload-skip', 'docker-auth-masked-skip'):
        if broken == 'docker-auth-masked-skip':
            skipped = []
        skipped.append({'source': {'kit': 'workload', 'path': 'capabilities[0]'}, 'path': 'capabilities[0]'})
    print(json.dumps({'selection': {'selected': [], 'skipped': skipped}, 'surface': {}}))
    sys.exit(0)
binding = data['binding']
login = binding['bindings'].get('registry.kit-tck.test', {})

spec = importlib.util.spec_from_file_location('auth_probe', pathlib.Path(__file__).parent / 'fixtures/workload/kit-tck-docker-auth.py')
probe = importlib.util.module_from_spec(spec)
sys.dont_write_bytecode = True
spec.loader.exec_module(probe)


def guest_config():
    if broken == 'docker-auth-hidden-config-leak' and not data['granted']:
        return {'auths': {'unrelated.example.com': {'identitytoken': login.get('password', '')}}}, []
    if not data['granted']:
        return None, []
    pair = [data['user'], data['password']]
    if broken == 'docker-auth-login-leak':
        pair = [login['username'], login['password']]
    encoded = base64.b64encode(':'.join(pair).encode()).decode()
    config = {'auths': {'registry.kit-tck.test': {'auth': encoded}}}
    if broken == 'docker-auth-helper-config':
        config = {'credHelpers': {'registry.kit-tck.test': 'fixture'}}
    if broken == 'docker-auth-unrelated-login':
        other = binding['bindings']['other-registry.kit-tck.test']
        config['auths']['other-registry.kit-tck.test'] = {'auth': base64.b64encode((other['username'] + ':' + other['password']).encode()).decode()}
    if broken == 'docker-auth-url-config':
        config = {'auths': {'https://registry.kit-tck.test/v1/': {'auth': encoded}}}
    return config, pair


# Expose the configuration as files so the probe's real resolver, including the
# credential-helper protocol, is exercised rather than replaced.
config, pair = guest_config()
guest.mkdir(exist_ok=True)
if config is not None:
    (guest / 'config.json').write_text(json.dumps(config))
    (guest / 'helper.json').write_text(json.dumps({'Username': pair[0], 'Secret': pair[1]}) if pair else '{}')
    helper = guest / 'docker-credential-fixture'
    helper.write_text('#!/bin/sh\nexec cat "$(dirname "$0")/helper.json"\n')
    helper.chmod(0o755)
os.environ['DOCKER_CONFIG'] = str(guest)
os.environ['PATH'] = str(guest) + os.pathsep + os.environ.get('PATH', '')


class RoutedHTTPS(http.client.HTTPSConnection):
    def connect(self):
        address, port = binding['routes'][self.host].rsplit(':', 1)
        raw = socket.create_connection((address, int(port)), timeout=10)
        context = ssl.create_default_context(cafile=binding['trust'])
        if broken == 'docker-auth-unverified-tls':
            context = ssl._create_unverified_context()
        self.sock = context.wrap_socket(raw, server_hostname=self.host)


def origin(url, authorization, method):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme == 'https':
        connection = RoutedHTTPS(parsed.hostname, timeout=10)
    else:
        address, port = binding['httpRoutes'][parsed.hostname].rsplit(':', 1)
        connection = http.client.HTTPConnection(address, int(port), timeout=10)
    try:
        path = parsed.path + ('?' + parsed.query if parsed.query else '')
        connection.request(method, path, headers={'Host': parsed.hostname, 'Authorization': authorization})
        response = connection.getresponse()
        return response.status, probe.header_values(response.getheaders()), response.read().decode('utf-8', errors='backslashreplace')
    finally:
        connection.close()


def mediated_request(url, authorization='', method='GET'):
    parsed = urllib.parse.urlsplit(url)
    host, path = parsed.hostname, parsed.path
    if broken == 'docker-auth-rewritten-realm' and path == '/v2/auth-exchange':
        if not parsed.query.startswith(OPAQUE_REALM_STATE + '&'):
            return 400, {}, ''
        return mediated_request('https://auth.kit-tck.test/token?' + parsed.query[len(OPAQUE_REALM_STATE) + 1:], authorization, method)
    if not data['network'] and broken != 'docker-auth-egress':
        return 403, {}, ''
    if host == 'denied.kit-tck.test' and broken != 'docker-auth-denied-redirect':
        return 403, {}, ''
    if host == 'auth.kit-tck.test' and method == 'POST' and broken != 'docker-auth-login-policy':
        return 403, {}, ''
    if ((host == 'registry.kit-tck.test' and method == 'DELETE' and broken != 'docker-auth-bearer-policy') or
            (host == 'cdn.kit-tck.test' and path == '/blocked' and broken != 'docker-auth-redirect-policy')):
        return 403, {}, ''
    if path.startswith('/v2/held/'):
        entry = data['locations'].get(path)
        if not entry:
            return 502, {}, ''
        return mediated_request(entry, authorization, method)
    if broken == 'docker-auth-reuses-login' and authorization.startswith('Basic '):
        data['user'], data['password'] = json.loads((root / 'old-login.json').read_text())
    if data['granted'] and authorization.startswith('Basic '):
        expected = 'Basic ' + base64.b64encode((data['user'] + ':' + data['password']).encode()).decode()
        if authorization != expected:
            return 401, {}, ''
        if (parsed.scheme == 'https' or broken == 'docker-auth-login-plaintext') and (host == 'auth.kit-tck.test' and path == '/token' and (urllib.parse.parse_qs(parsed.query).get('service') == ['kit-tck-registry'] or broken == 'docker-auth-service-scope')) or broken == 'docker-auth-login-scope':
            authorization = 'Basic ' + base64.b64encode((login.get('username', '') + ':' + login.get('password', '')).encode()).decode()
    if authorization.startswith('Bearer '):
        placeholder = authorization[7:]
        token = data['tokens'].get(placeholder)
        if broken == 'docker-auth-unknown-bearer' and token is None and data['tokens']:
            token = next(iter(data['tokens'].values()))
        if token and (parsed.scheme == 'https' or broken == 'docker-auth-plaintext') and ((host == 'registry.kit-tck.test' and (path.startswith('/v2/') or broken == 'docker-auth-bearer-path')) or broken == 'docker-auth-bearer-scope' or broken == 'docker-auth-cdn-bearer'):
            if time.time() < token['expires'] or broken == 'docker-auth-expiry':
                authorization = 'Bearer ' + token['secret']
            elif broken == 'docker-auth-renews-token':
                basic = 'Basic ' + base64.b64encode((login['username'] + ':' + login['password']).encode()).decode()
                _, _, renewed_body = origin('https://auth.kit-tck.test/token?service=kit-tck-registry', basic, 'GET')
                renewed = json.loads(renewed_body)
                token['secret'] = renewed['token']
                token['expires'] = time.time() + renewed['expires_in']
                authorization = 'Bearer ' + token['secret']
            else:
                return 401, {}, ''
        elif host == 'cdn.kit-tck.test':
            authorization = ''
        elif broken == 'docker-auth-reuses-run' and host == 'registry.kit-tck.test':
            # A broken runtime shares its host token store across sandbox runs.
            for candidate in root.glob('global-*.json'):
                shared = json.loads(candidate.read_text())
                if placeholder in shared:
                    authorization = 'Bearer ' + shared[placeholder]['secret']
    if broken in ('docker-auth-login-scope', 'docker-auth-login-plaintext', 'docker-auth-service-scope',
                  'docker-auth-bearer-scope', 'docker-auth-bearer-path', 'docker-auth-plaintext', 'docker-auth-expiry'):
        scheme, separator, credential = authorization.partition(' ')
        authorization = scheme.lower() + separator + credential
    try:
        status, headers, body = origin(url, authorization, method)
    except (OSError, http.client.HTTPException):
        return 502, {}, ''
    if host == 'auth.kit-tck.test' and path == '/token' and data['granted']:
        if status != 200:
            return (status, headers, body) if broken == 'docker-auth-token-leak' else (502, {}, '')
        try:
            payload = json.loads(body)
            if body.count('"token"') > 1:
                raise ValueError('duplicate')
        except ValueError:
            return (200, headers, body) if broken == 'docker-auth-token-leak' else (502, {}, '')
        if broken == 'docker-auth-token-leak':
            return status, headers, body
        leaked_headers = headers if broken == 'docker-auth-header-leak' else {}
        placeholder = 'bearer-placeholder-' + uuid.uuid4().hex
        data['tokens'][placeholder] = {'secret': payload['token'], 'expires': time.time() + payload['expires_in']}
        if broken == 'docker-auth-reuses-run':
            (root / ('global-' + args[0] + '.json')).write_text(json.dumps(data['tokens']))
        fields = {'access_token': placeholder}
        if broken != 'docker-auth-access-token-only':
            fields['token'] = placeholder
        if broken == 'docker-auth-escaped-token':
            fields['refresh_token'] = json.loads(body)['refresh_token']
            return 200, {}, json.dumps(fields).replace('refresh-', r'\u0072efresh-')[:-1] + ', "refresh_token":"redacted"}'
        return 200, leaked_headers, json.dumps(fields)
    if broken == 'docker-auth-rewritten-realm' and status == 401 and host == 'registry.kit-tck.test':
        headers['www-authenticate'] = ['Bearer realm="https://registry.kit-tck.test/v2/auth-exchange?' + OPAQUE_REALM_STATE + '",service="kit-tck-registry"']
    location = next(iter(headers.get('location', [])), '')
    if location and broken == 'docker-auth-encoded-location':
        return status, {'location': [urllib.parse.urljoin(url, location).replace('signature-', '%73ignature-')]}, body
    if location and broken == 'docker-auth-host-redirect' and path == '/v2/blob':
        return mediated_request(urllib.parse.urljoin(url, location), 'Bearer ' + placeholder, method)
    if location and broken != 'docker-auth-location-leak' and not (broken == 'docker-auth-upload-leak' and path.startswith('/v2/uploads/')):
        held = '/v2/held/' + uuid.uuid4().hex
        data['locations'][held] = urllib.parse.urljoin(url, location)
        headers = {'location': [held if broken == 'docker-auth-relative-location' else 'https://registry.kit-tck.test' + held]}
        body = ''
    return status, headers, body


probe.transport_request = mediated_request
request = probe.request
probe.pathlib = pathlib
mode = args[3]
try:
    if mode in ('save', 'save-short'):
        result = {'token': probe.token_value(probe.exchange('short' if mode == 'save-short' else '')[5]), 'pair': probe.credentials()[0]}
        data['saved'] = result['token']
    elif mode == 'saved':
        result = request('https://registry.kit-tck.test/v2/', 'Bearer ' + data['saved'])
    elif mode == 'foreign':
        result = [request('https://registry.kit-tck.test/v2/', 'Bearer ' + args[4]),
                  probe.exchange(pair=args[5:7])[3:]]
    elif mode == 'ambient':
        if broken == 'docker-auth-ambient-fails':
            sys.exit(1)
        result = probe.observe(mode)
        result.update(socket=data['granted'] and broken == 'docker-auth-socket', engine=False)
    elif mode == 'hooks':
        records = {}
        for phase in ('install', 'startup'):
            probe.evidence.clear()
            records[phase] = {'result': probe.observe('login'), 'responses': list(probe.evidence)}
        if broken == 'docker-auth-late-hooks':
            records['install']['result'] = {}
        probe.evidence.clear()
        result = probe.hook_results(records)
    else:
        result = probe.observe(mode)
        if broken == 'docker-auth-missing-observation' and mode == 'bearer':
            del result['unknown']
    probe.emit(result)
except (OSError, ValueError, KeyError, IndexError):
    sys.exit(1)
finally:
    file.write_text(json.dumps(data))
