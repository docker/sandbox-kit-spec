import base64
import importlib.util
import json
import io
from email.message import Message
import os
import pathlib
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('probe', pathlib.Path(__file__).with_name('kit-tck-docker-auth.py'))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class CredentialProbeTests(unittest.TestCase):
    def test_inline_helper_and_store(self):
        registry = 'registry.kit-tck.test'
        configs = [
            {'auths': {registry: {'auth': base64.b64encode(b'user:placeholder').decode()}}},
            {'auths': {'https://' + registry + '/v1/': {'auth': base64.b64encode(b'user:placeholder').decode()}}},
            {'credHelpers': {registry: 'fixture'}, 'credsStore': 'not-selected'},
            {'credsStore': 'fixture'},
        ]
        for config in configs:
            with self.subTest(config=config), tempfile.TemporaryDirectory() as directory:
                pathlib.Path(directory, 'config.json').write_text(json.dumps(config))
                helper = mock.Mock(stdout=json.dumps({'Username': 'user', 'Secret': 'placeholder'}))
                with mock.patch.dict(os.environ, {'DOCKER_CONFIG': directory}), mock.patch.object(probe.subprocess, 'run', return_value=helper) as run:
                    pair, _ = probe.credentials()
                    self.assertEqual(pair, ['user', 'placeholder'])
                    if 'auths' not in config:
                        self.assertEqual(run.call_args.args[0], ['docker-credential-fixture', 'get'])
                        self.assertEqual(run.call_args.kwargs['input'], registry + '\n')
                    else:
                        run.assert_not_called()

    def test_missing_helper_credential_is_unavailable(self):
        for failure in (probe.subprocess.CalledProcessError(1, ['docker-credential-fixture']), probe.subprocess.TimeoutExpired('docker-credential-fixture', 10)):
            with self.subTest(failure=type(failure).__name__), mock.patch.object(probe, 'docker_config', return_value={'credsStore': 'fixture'}), mock.patch.object(probe, 'credentials', side_effect=failure), mock.patch.object(probe, 'request', return_value=(401, {}, '')):
                result = probe.observe('ambient')
                self.assertEqual(result['config'], {'credsStore': 'fixture'})
                self.assertIsNone(result['authentication'])

    def test_ambient_reports_config_without_resolvable_credentials(self):
        for config in ({'auths': {'registry.kit-tck.test': {'identitytoken': 'secret-canary'}}},
                       {'auths': {'another.example.com': {'auth': 'encoded-secret-canary'}}}):
            with mock.patch.object(probe, 'docker_config', return_value=config), mock.patch.object(probe, 'request', return_value=(401, {}, '')):
                result = probe.observe('ambient')
                self.assertEqual(result['config'], config)
                self.assertIsNone(result['authentication'])

    def test_response_evidence_is_lossless(self):
        probe.evidence.clear()
        response = mock.Mock(code=401, headers=Message(), read=io.BytesIO(b'\xfferror-body').read)
        response.headers['X-Secret'] = 'first'
        response.headers['x-secret'] = 'second'
        with mock.patch.object(probe, 'transport_request', return_value=probe.response_values(response)):
            probe.request('https://registry.kit-tck.test/v2/')
        self.assertEqual(probe.evidence[0]['headers'], {'x-secret': ['first', 'second']})
        self.assertEqual(probe.evidence[0]['body'], '\\xfferror-body')
        truncated = mock.Mock(code=200, headers=Message(), read=mock.Mock(side_effect=[b'partial-', probe.http.client.IncompleteRead(b'secret')]))
        self.assertEqual(probe.response_values(truncated), (0, {}, 'partial-secret'))

    def test_token_fields(self):
        for response in ({'token': 'placeholder'}, {'access_token': 'placeholder'}, {'token': '', 'access_token': 'placeholder'}):
            self.assertEqual(probe.token_value(json.dumps(response)), 'placeholder')
        for response in ({}, {'access_token': None}):
            with self.assertRaises(ValueError):
                probe.token_value(json.dumps(response))

    def test_relative_redirects(self):
        def request(url, authorization='', method='GET'):
            if '/v2/blob' in url:
                return 307, {'location': ['/v2/held/opaque']}, ''
            self.assertEqual(url, 'https://registry.kit-tck.test/v2/held/opaque')
            return 200, {}, 'content'
        with mock.patch.object(probe, 'exchange', return_value=([], {}, '', 200, {}, '{"access_token":"placeholder"}')), mock.patch.object(probe, 'request', side_effect=request):
            result = probe.observe('redirect')
            self.assertTrue(all(item['followed'][0] == 200 for item in result))

    def test_saved_replay_never_exchanges(self):
        with tempfile.TemporaryDirectory() as directory:
            saved = pathlib.Path(directory, 'saved')
            saved.write_text('saved-placeholder')
            with mock.patch.object(probe.pathlib, 'Path', return_value=saved), mock.patch.object(probe, 'exchange', side_effect=AssertionError('unexpected exchange')), mock.patch.object(probe, 'request', return_value=(401, {}, '')) as request:
                self.assertEqual(probe.observe('saved')[0], 401)
                request.assert_called_once_with('https://registry.kit-tck.test/v2/', 'Bearer saved-placeholder')


if __name__ == '__main__':
    unittest.main()
