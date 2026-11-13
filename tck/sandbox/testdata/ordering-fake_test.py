"""Exercise shutdown observations with real signals and process lifetimes."""
import json
import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import time
import unittest

HERE = pathlib.Path(__file__).resolve().parent


class OrderingProcesses(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = pathlib.Path(directory.name)

    def wait_for(self, predicate):
        deadline = time.monotonic() + 5
        while not predicate():
            if time.monotonic() >= deadline:
                self.fail('fixture did not reach the expected process state')
            time.sleep(0.01)

    def child(self, code, *args):
        process = subprocess.Popen([sys.executable, '-c', code, *map(str, args)])

        def cleanup():
            if process.poll() is None:
                process.kill()
            process.wait(timeout=5)

        # A failing assertion must not leave a detached fixture running.
        self.addCleanup(cleanup)
        return process

    def test_workload_handles_stop_when_readiness_is_published(self):
        source = HERE / 'fixtures/ordering-workload/probe.py'
        probe = self.root / 'probe.py'
        probe.write_text(source.read_text().replace("'/var/tmp'", repr(str(self.root))))
        # Deliver a real SIGTERM immediately after the readiness record
        # closes. This exposes the registration race without relying on
        # scheduler timing or a repeated stress test.
        result = subprocess.run([sys.executable, '-c', '''
import contextlib
import os
import pathlib
import runpy
import signal
import sys

original_open = pathlib.Path.open

@contextlib.contextmanager
def stop_after_ready(path, *args, **kwargs):
    with original_open(path, *args, **kwargs) as record:
        yield record
    if path.name == 'ordering-entrypoint':
        os.kill(os.getpid(), signal.SIGTERM)

pathlib.Path.open = stop_after_ready
sys.argv = sys.argv[1:]
runpy.run_path(sys.argv[0], run_name='__main__')
''', str(probe), 'workload'], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.root / 'ordering-stop').read_text(), 'workload,')

    def test_stop_handles_live_zombie_and_reaped_processes_without_procfs(self):
        ready, stopped = self.root / 'ready', self.root / 'stopped'
        live = self.child('''
import pathlib
import signal
import sys

def stop(_sig, _frame):
    pathlib.Path(sys.argv[2]).write_text('stopped')
    sys.exit(0)

signal.signal(signal.SIGTERM, stop)
pathlib.Path(sys.argv[1]).touch()
while True:
    signal.pause()
''', ready, stopped)
        self.wait_for(ready.exists)

        # Keep this Popen object alive without poll()/wait() until after
        # Stop. It leaves a real zombie, which kill(pid, 0) would report
        # as present even though it cannot run a shutdown handler.
        zombie = self.child('pass')
        self.wait_for(lambda: subprocess.run(
            ['ps', '-p', str(zombie.pid), '-o', 'stat='],
            capture_output=True, text=True, check=True,
        ).stdout.strip().startswith('Z'))
        reaped = self.child('pass')
        self.assertEqual(reaped.wait(timeout=5), 0)

        directory = self.root / 'ordering' / 'sandbox'
        directory.mkdir(parents=True)
        state = directory / 'state.json'
        state.write_text(json.dumps({
            'plan': {'stop': []},
            'processes': [{'pid': p.pid} for p in (reaped, zombie, live)],
        }))
        # Stop runs in another interpreter, so none of the stored PIDs
        # are its children. Hide direct procfs reads even on Linux to
        # exercise hosts such as macOS without replacing OS process data.
        result = subprocess.run([sys.executable, '-c', '''
import pathlib
import runpy
import sys

original_read = pathlib.Path.read_text

def without_procfs(path, *args, **kwargs):
    if path.is_relative_to('/proc'):
        raise FileNotFoundError(path)
    return original_read(path, *args, **kwargs)

pathlib.Path.read_text = without_procfs
sys.argv = sys.argv[1:]
runpy.run_path(sys.argv[0], run_name='__main__')
''', str(HERE / 'ordering-fake.py'), str(self.root), '', '', 'stop', 'sandbox'],
            capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(stopped.exists(), 'Stop forgot a live process without signaling it')
        self.assertEqual(live.wait(timeout=5), 0)
        self.assertEqual(json.loads(state.read_text())['processes'], [])

    def test_failed_start_retains_service_for_later_stop(self):
        identifier = self.root.name
        directory = self.root / 'ordering' / identifier
        directory.mkdir(parents=True)
        ready, stopped, command_record = (self.root / name for name in ('ready', 'stopped', 'commands'))

        def action(name, command, background=False):
            return {'entryId': name, 'fragmentId': name, 'phase': 'start',
                    'step': 'execute', 'action': 'startup',
                    'capability': {'type': 'com.docker.sandbox/lifecycle@1',
                                   'config': {'startup': [{'command': command, 'background': background}]}}}

        completed = action('completed', ['sh', '-c', 'printf once, >> ' + str(command_record)])
        service = action('service', [sys.executable, '-c', '''
import os
import pathlib
import signal
import sys

def stop(_sig, _frame):
    pathlib.Path(sys.argv[2]).write_text('service,')
    sys.exit(0)

signal.signal(signal.SIGTERM, stop)
pathlib.Path(sys.argv[1]).write_text(str(os.getpid()))
while True:
    signal.pause()
''', str(ready), str(stopped)], True)
        failing = action('failing', ['sh', '-c', 'while [ ! -s ' + str(ready) + ' ]; do sleep 0.01; done; exit 23'])
        actions = [completed, service, failing]
        state_file = directory / 'state.json'
        state_file.write_text(json.dumps({
            'plan': {'phases': [{'phase': 'start', 'steps': [{'step': 'execute', 'actions': actions}]}],
                     'stop': list(reversed(actions)), 'installEnd': []},
            'workspace': '', 'installed': True, 'active': [], 'boots': 1, 'processes': [],
        }))
        start = subprocess.run([sys.executable, str(HERE / 'ordering-fake.py'),
                                str(self.root), '', '', 'start', identifier],
                               capture_output=True, text=True, timeout=10)
        self.assertNotEqual(start.returncode, 0, 'the later startup command must fail')
        self.assertTrue(ready.exists(), start.stderr)
        pid = int(ready.read_text())

        def cleanup():
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass

        self.addCleanup(cleanup)
        persisted = json.loads(state_file.read_text())
        self.assertEqual(persisted['active'], ['service/service/start/execute/startup'])
        self.assertFalse(stopped.exists(), 'the service remains active until Stop')
        stop = subprocess.run([sys.executable, str(HERE / 'ordering-fake.py'),
                               str(self.root), '', '', 'stop', identifier],
                              capture_output=True, text=True, timeout=10)
        self.assertEqual(stop.returncode, 0, stop.stderr)
        self.assertEqual(stopped.read_text(), 'service,')
        self.assertEqual(command_record.read_text(), 'once,', 'cleanup must not rerun completed commands')
        self.assertEqual(json.loads(state_file.read_text())['active'], [])

    def test_stop_uses_reached_phase_and_skips_withdrawn_install(self):
        for reached in ('install', 'start'):
            with self.subTest(reached=reached):
                directory = self.root / reached
                directory.mkdir()
                stopped = directory / 'stopped'
                actions, processes, active = [], [], []
                for phase in ('prepare', 'install', 'start', 'running'):
                    action = {'entryId': 'grant', 'fragmentId': 'grant',
                              'phase': phase, 'step': 'attach', 'action': 'access'}
                    actions.insert(0, action)
                    # Install has already withdrawn its grant before Start.
                    # Unreached phases never began an effect to withdraw.
                    if phase != 'prepare' and phase != reached:
                        continue
                    ready = directory / phase
                    process = self.child('''
import pathlib
import signal
import sys

def stop(_sig, _frame):
    with pathlib.Path(sys.argv[2]).open('a') as output:
        output.write(sys.argv[3] + ',')
    sys.exit(0)

signal.signal(signal.SIGTERM, stop)
pathlib.Path(sys.argv[1]).touch()
while True:
    signal.pause()
''', ready, stopped, phase)
                    self.wait_for(ready.exists)
                    identity = 'grant/grant/' + phase + '/attach/access'
                    active.append(identity)
                    processes.append({'pid': process.pid, 'action': identity})
                state_dir = self.root / 'ordering' / reached
                state_dir.mkdir(parents=True)
                state = state_dir / 'state.json'
                state.write_text(json.dumps({'plan': {'stop': actions},
                                             'active': active, 'processes': processes}))
                result = subprocess.run([sys.executable, str(HERE / 'ordering-fake.py'),
                                         str(self.root), '', '', 'stop', reached],
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(stopped.read_text(), reached + ',prepare,')
                self.assertEqual(json.loads(state.read_text())['active'], [])


if __name__ == '__main__':
    unittest.main()
