#!/usr/bin/env python3
"""Observe client-visible credentials and HTTPS exchanges; never report real host bindings."""
import base64
import http.client
import json
import os
import pathlib
import sys
import subprocess
import urllib.error
import urllib.request
import urllib.parse


def docker_config():
    directory = pathlib.Path(os.environ.get('DOCKER_CONFIG', str(pathlib.Path.home() / '.docker')))
    return json.loads((directory / 'config.json').read_text())


def credentials():
    config = docker_config()
    registry = 'registry.kit-tck.test'
    helper = config.get('credHelpers', {}).get(registry) or config.get('credsStore')
    if helper:
        result = subprocess.run(['docker-credential-' + helper, 'get'], input=registry + '\n', text=True, capture_output=True, check=True, timeout=10)
        binding = json.loads(result.stdout)
        pair = [binding['Username'], binding['Secret']]
    else:
        auths = config.get('auths', {})
        binding = auths.get(registry)
        if binding is None:
            binding = next((value for key, value in auths.items()
                            if urllib.parse.urlsplit(key if '://' in key else '//' + key).netloc == registry), {})
        pair = base64.b64decode(binding['auth'], validate=True).decode().split(':', 1)
    if len(pair) != 2 or not all(isinstance(value, str) and value for value in pair):
        raise ValueError('invalid placeholder credentials')
    return pair, config


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
        # Keep bytes already exposed, but never report a truncated response as complete.
        status, body = 0, body + getattr(error, 'partial', b'')
    return status, header_values(response.headers.items()), body.decode('utf-8', errors='backslashreplace')


def transport_request(url, authorization='', method='GET'):
    headers = {'Authorization': authorization} if authorization else {}
    req = urllib.request.Request(url, headers=headers, method=method)
    try:
        response = urllib.request.build_opener(NoRedirect()).open(req, timeout=10)
    except urllib.error.HTTPError as response:
        with response:
            return response_values(response)
    except (OSError, http.client.HTTPException):
        return 0, {}, ''
    with response:
        return response_values(response)


# Preserve every client-visible response, including discovery and failed
# exchanges that a higher-level operation might otherwise discard.
evidence = []


def request(url, authorization='', method='GET'):
    status, headers, body = transport_request(url, authorization, method)
    evidence.append({'url': url, 'method': method, 'status': status, 'headers': headers, 'body': body})
    return status, headers, body


def emit(result):
    print(json.dumps({'result': result, 'responses': evidence}))


def exchange(case='', pair=None):
    config = None
    if pair is None:
        pair, config = credentials()
    _, headers, _ = request('https://registry.kit-tck.test/v2/')
    challenge = next(value for value in headers.get('www-authenticate', []) if value.lower().startswith('bearer '))
    parameters = urllib.request.parse_keqv_list(urllib.request.parse_http_list(challenge.split(' ', 1)[1]))
    realm = urllib.parse.urlsplit(parameters['realm'])
    if realm.scheme != 'https' or realm.username or realm.password:
        raise ValueError('invalid authentication realm')
    # Realm query state may be opaque or signed: preserve it verbatim and only
    # append protocol parameters.
    extra = urllib.parse.urlencode({key: value for key, value in (('service', parameters.get('service')), ('case', case)) if value})
    url = urllib.parse.urlunsplit(realm._replace(query='&'.join(part for part in (realm.query, extra) if part)))
    basic = 'Basic ' + base64.b64encode(':'.join(pair).encode()).decode()
    status, headers, body = request(url, basic)
    return pair, config, basic, status, headers, body


def token_value(body):
    response = json.loads(body)
    token = response.get('token') or response.get('access_token')
    if not isinstance(token, str) or not token:
        raise ValueError('missing token placeholder')
    return token


def hook_results(records):
    results = {}
    for phase, record in records.items():
        evidence.extend(record['responses'])
        results[phase] = record['result']
    return results


def observe(mode):
    if mode == 'saved':
        saved = pathlib.Path('/var/tmp/kit-tck-docker-auth-saved').read_text()
        return request('https://registry.kit-tck.test/v2/', 'Bearer ' + saved)
    if mode == 'foreign':
        return [request('https://registry.kit-tck.test/v2/', 'Bearer ' + sys.argv[2]),
                exchange(pair=sys.argv[3:5])[3:]]
    if mode == 'config':
        pair, config = credentials()
        return {'pair': pair, 'config': config}
    if mode == 'ambient':
        result = {'config': None, 'socket': pathlib.Path('/var/run/docker.sock').exists(),
                  'engine': os.system('command -v dockerd >/dev/null 2>&1') == 0}
        try:
            result['config'] = docker_config()
        except (OSError, ValueError, KeyError, StopIteration, subprocess.SubprocessError):
            pass
        result['request'] = request('https://registry.kit-tck.test/v2/')
        try:
            result['authentication'] = observe('login')
        except (OSError, ValueError, KeyError, StopIteration, subprocess.SubprocessError):
            result['authentication'] = None
        return result
    if mode == 'hooks':
        return hook_results({phase: json.loads(pathlib.Path('/var/tmp/kit-tck-docker-auth-' + phase).read_text())
                             for phase in ('install', 'startup')})
    if mode == 'mediation':
        return {case: exchange(case)[3:] for case in ('', 'error', 'redirect', 'malformed')}
    pair, config, basic, status, headers, body = exchange('short' if mode == 'save-short' else '')
    token = token_value(body)
    bearer = 'Bearer ' + token
    if mode == 'login':
        return {'exchange': [status, headers, body], 'registry': request('https://registry.kit-tck.test/v2/', bearer)}
    if mode == 'scope':
        return [request('https://auth.kit-tck.test/token?service=kit-tck-registry', basic, 'POST')] + [request(url, basic) for url in (
            'https://auth.kit-tck.test/not-token', 'https://other-registry.kit-tck.test/token',
            'https://auth.kit-tck.test/token?service=ungranted-registry',
            'http://auth.kit-tck.test/token?service=kit-tck-registry')]
    if mode == 'bearer':
        return {'unknown': request('https://registry.kit-tck.test/v2/', 'Bearer kit-tck-unknown-placeholder'),
                'token': token, 'valid': request('https://registry.kit-tck.test/v2/', bearer),
                'other': request('https://other-registry.kit-tck.test/v2/', bearer),
                'non_api': request('https://registry.kit-tck.test/not-api', bearer),
                'plaintext': request('http://registry.kit-tck.test/v2/', bearer),
                'denied': request('https://registry.kit-tck.test/v2/', bearer, 'DELETE')}
    if mode == 'redirect':
        results = []
        for suffix in ('', '?deny=true', '?path-deny=true'):
            url = 'https://registry.kit-tck.test/v2/blob' + suffix
            code, headers, body = request(url, bearer)
            location = next(iter(headers.get('location', [])), '')
            followed = request(urllib.parse.urljoin(url, location), bearer) if location else None
            results.append({'status': code, 'location': location, 'body': body, 'followed': followed})
        return results
    if mode == 'upload':
        results = []
        url = 'https://registry.kit-tck.test/v2/uploads/'
        for method in ('POST', 'PUT'):
            code, headers, body = request(url, bearer, method)
            location = next(iter(headers.get('location', [])), '')
            results.append({'status': code, 'location': location, 'body': body})
            if location:
                url = urllib.parse.urljoin(url, location)
        return results
    if mode in ('save', 'save-short'):
        pathlib.Path('/var/tmp/kit-tck-docker-auth-saved').write_text(token)
        return {'token': token, 'pair': pair}
    raise ValueError('unknown mode')


if __name__ == '__main__':
    try:
        emit(observe(sys.argv[1]))
    except Exception as error:
        emit({'error': type(error).__name__})
        print(type(error).__name__, file=sys.stderr)
        sys.exit(1)
