#!/usr/bin/env python3
"""Observe the credentials and HTTP exchanges a Docker-compatible client sees."""
import base64
import http.client
import json
import os
import pathlib
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request

REGISTRY = 'registry.kit-tck.test'


def docker_config():
    directory = pathlib.Path(os.environ.get('DOCKER_CONFIG', str(pathlib.Path.home() / '.docker')))
    return json.loads((directory / 'config.json').read_text())


def credentials(registry=REGISTRY):
    config = docker_config()
    helper = config.get('credHelpers', {}).get(registry) or config.get('credsStore')
    if helper:
        result = subprocess.run(['docker-credential-' + helper, 'get'], input=registry + '\n',
                                text=True, capture_output=True, check=True, timeout=10)
        binding = json.loads(result.stdout)
        pair = [binding['Username'], binding['Secret']]
    else:
        auths = config.get('auths', {})
        binding = auths.get(registry) or next((value for key, value in auths.items()
                                               if urllib.parse.urlsplit(key if '://' in key else '//' + key).netloc == registry), {})
        pair = ['<token>', binding['identitytoken']] if binding.get('identitytoken') else base64.b64decode(binding['auth'], validate=True).decode().split(':', 1)
    if len(pair) != 2 or not all(isinstance(value, str) and value for value in pair):
        raise ValueError('invalid placeholder credentials')
    return pair


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def header_values(items):
    headers = {}
    for name, value in items:
        headers.setdefault(name.lower(), []).append(value)
    return headers


def response_values(response):
    status, body = response.code, b''
    try:
        while chunk := response.read(65536):
            body += chunk
    except (OSError, http.client.HTTPException) as error:
        # Truncation is not success, but already exposed bytes remain evidence.
        status, body = 0, body + getattr(error, 'partial', b'')
    expected = response.headers.get('Content-Length')
    if expected is not None and not response.headers.get('Transfer-Encoding'):
        try:
            if len(body) != int(expected):
                status = 0
        except ValueError:
            status = 0
    return status, header_values(response.headers.items()), body.decode('utf-8', 'backslashreplace')


def transport_request(url, authorization='', method='GET', data=None):
    headers = {'Authorization': authorization} if authorization else {}
    req = urllib.request.Request(url, headers=headers, method=method, data=data)
    if data is not None:
        req.add_header('Content-Type', 'application/x-www-form-urlencoded')
    try:
        response = urllib.request.build_opener(NoRedirect()).open(req, timeout=10)
    except urllib.error.HTTPError as response:
        with response:
            return response_values(response)
    except (OSError, http.client.HTTPException):
        return 0, {}, ''
    with response:
        return response_values(response)


def request(url, authorization='', method='GET'):
    status, headers, body = transport_request(url, authorization, method)
    return {'status': status, 'headers': headers, 'body': body}


def basic(pair):
    return 'Basic ' + base64.b64encode(':'.join(pair).encode()).decode()


def challenge(path='/v2/'):
    """Discover and verify the registry's authentication challenge."""
    _, headers, _ = transport_request('https://' + REGISTRY + path)
    value = next((value for value in headers.get('www-authenticate', []) if value.lower().startswith('bearer ')),
                 None)
    if value is None:
        value = os.environ['KIT_TCK_AUTH_CHALLENGE']
        if path == '/v2/replay':
            value = value.replace('/token?', '/token/replay?', 1)
    parameters = urllib.request.parse_keqv_list(urllib.request.parse_http_list(value.split(' ', 1)[1]))
    realm = urllib.parse.urlsplit(parameters['realm'])
    if realm.scheme != 'https' or realm.username or realm.password:
        raise ValueError('invalid authentication realm')
    return realm, parameters.get('service')


def realm_url(realm, host=None, path=None, service=None):
    """Vary one admission component at a time, preserving the realm's own
    query state verbatim and only appending protocol parameters."""
    target = realm._replace(netloc=host or realm.netloc, path=path or realm.path)
    extra = urllib.parse.urlencode({'service': service}) if service else ''
    return urllib.parse.urlunsplit(target._replace(query='&'.join(part for part in (target.query, extra) if part)))


def login_request(url, pair):
    if pair[0] == '<token>':
        service = urllib.parse.parse_qs(urllib.parse.urlsplit(url).query).get('service', [''])[0]
        data = urllib.parse.urlencode({'grant_type': 'refresh_token', 'refresh_token': pair[1], 'service': service}).encode()
        return transport_request(url, method='POST', data=data)
    return transport_request(url, basic(pair))


def exchange(pair=None):
    realm, service = challenge()
    return login_request(realm_url(realm, service=service), pair or credentials())


def exchange_token(pair=None):
    """A Docker-compatible client uses a token only from a successful exchange."""
    status, _, body = exchange(pair)
    if not 200 <= status < 300:
        raise ValueError('token exchange failed')
    return token_value(body)


def token_value(body):
    response = json.loads(body)
    token = response.get('token') or response.get('access_token')
    if not isinstance(token, str) or not token:
        raise ValueError('missing token')
    return token


def daemon_state():
    running = False
    for entry in pathlib.Path('/proc').glob('[0-9]*/comm'):
        try:
            running = running or entry.read_text().strip() == 'dockerd'
        except FileNotFoundError:
            pass  # process exited during enumeration
    try:
        result = subprocess.run(['docker', 'info', '--format', '{{.ID}}'], capture_output=True, timeout=5)
        reachable = result.returncode == 0
    except FileNotFoundError:
        reachable = False
    except subprocess.TimeoutExpired:
        reachable = False
    return {'daemon': running, 'endpoint': reachable}


def observe(mode):
    if mode == 'login':
        request('https://cdn.kit-tck.test/token')
        other = []
        try:
            other = credentials('cdn.kit-tck.test')
        except (OSError, ValueError, KeyError, StopIteration, subprocess.SubprocessError):
            pass
        if other:
            login_request('https://cdn.kit-tck.test/token', other)
        pair = credentials()
        status, headers, body = exchange(pair=pair)
        if not 200 <= status < 300:
            raise ValueError('token exchange failed')
        token = token_value(body)
        return {'pair': pair, 'other': other, 'config': docker_config(), 'token': token, 'exchange': {'headers': headers, 'body': body},
                'registry': request('https://' + REGISTRY + '/v2/', 'Bearer ' + token)}
    if mode == 'error':
        status, headers, body = exchange()
        return {'status': status, 'headers': headers, 'body': body}
    if mode == 'scope':
        pair = credentials()
        realm, service = challenge()
        return [login_request(realm_url(realm._replace(scheme='http'), service=service), pair)[0]] + [login_request(realm_url(realm, **variant), pair)[0] for variant in (
            {'host': 'cdn.kit-tck.test', 'service': service},
            {'path': '/not-token', 'service': service},
            {'service': 'ungranted-registry'})]
    if mode == 'bearer':
        token = exchange_token()
        return {'valid': request('https://' + REGISTRY + '/v2/', 'Bearer ' + token),
                'non_api': request('https://' + REGISTRY + '/not-api', 'Bearer ' + token),
                'cross': request('https://cdn.kit-tck.test/layer', 'Bearer ' + token),
                'plaintext': request('http://' + REGISTRY + '/v2/', 'Bearer ' + token)}
    if mode == 'unknown':
        return request('https://' + REGISTRY + '/v2/unknown', 'Bearer kit-tck-unknown-placeholder')
    if mode == 'discover':
        realm, service = challenge('/v2/replay')
        return realm_url(realm, service=service)
    if mode == 'short':
        return exchange_token()
    if mode == 'expired':
        return request('https://' + REGISTRY + '/v2/', 'Bearer ' + sys.argv[2])
    if mode == 'redirect':
        bearer = 'Bearer ' + exchange_token()

        def fetch(url, method='GET', follow='GET'):
            status, headers, body = transport_request(url, bearer, method)
            location = next(iter(headers.get('location', [])), '')
            followed = request(urllib.parse.urljoin(url, location), bearer, follow) if location else None
            return {'status': status, 'headers': headers, 'body': body, 'location': location, 'followed': followed}
        return {'allowed': fetch('https://' + REGISTRY + '/v2/blob'),
                'denied': fetch('https://' + REGISTRY + '/v2/blob?deny=true'),
                'upload': fetch('https://' + REGISTRY + '/v2/uploads/', 'POST', 'PUT')}
    if mode == 'ambient':
        try:
            credentials()
            resolved = True
        except (OSError, ValueError, KeyError, StopIteration, subprocess.SubprocessError):
            resolved = False
        return {'resolved': resolved,
                'socket': pathlib.Path('/var/run/docker.sock').exists(),
                'engine': os.system('command -v dockerd >/dev/null 2>&1') == 0,
                **daemon_state(),
                'request': request('https://' + REGISTRY + '/v2/')}
    if mode == 'no-egress':
        status, _, body = exchange()
        token = token_value(body) if 200 <= status < 300 else 'kit-tck-unknown-placeholder'
        return request('https://' + REGISTRY + '/v2/', 'Bearer ' + token)
    if mode == 'replay':
        status, _, body = login_request(sys.argv[5], sys.argv[3:5])
        return {'bearer': request('https://' + REGISTRY + '/v2/replay', 'Bearer ' + sys.argv[2]),
                'login': {'status': status, 'body': body}}
    if mode == 'hooks':
        return {phase: json.loads(pathlib.Path('/var/tmp/kit-tck-docker-auth-' + phase).read_text())
                for phase in ('install', 'startup')}
    raise ValueError('unknown mode')


if __name__ == '__main__':
    print(json.dumps(observe(sys.argv[1])))
