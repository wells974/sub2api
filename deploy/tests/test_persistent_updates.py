"""Exercise the real entrypoint in disposable containers with fake binaries."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


@unittest.skipUnless(shutil.which('docker'), 'Docker is required')
class PersistentUpdateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='sub2api-update-test-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.data = self.root / 'data'
        self.data.mkdir()
        self.data.chmod(0o777)
        self.binary = self.root / 'image-binary'
        self.write_binary(self.binary, 'image-v1')
        self.entrypoint = Path(__file__).resolve().parents[1] / 'docker-entrypoint.sh'

    @staticmethod
    def write_binary(path, version):
        path.write_text(f"""#!/bin/sh
printf '%s\\n' '{version}'
for arg in "$@"; do printf '<%s>\\n' "$arg"; done
""")
        path.chmod(0o755)

    def run_entrypoint(self, *args, persist=True, check=True):
        result = subprocess.run([
            'docker', 'run', '--rm', '--network', 'none', '--user',
            f'{os.getuid() or 1000}:{os.getgid() or 1000}',
            '-e', 'SUB2API_PERSIST_UPDATES=' + str(persist).lower(),
            '-v', f'{self.entrypoint}:/entrypoint.sh:ro',
            '-v', f'{self.binary}:/app/sub2api:ro',
            '-v', f'{self.data}:/app/data',
            '--entrypoint', '/bin/sh',
            os.environ.get('SUB2API_ENTRYPOINT_TEST_IMAGE', 'alpine:3.21'),
            '/entrypoint.sh', *(args or ('/app/sub2api',)),
        ], capture_output=True, text=True, timeout=45)
        if check:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def test_update_survives_container_recreation_and_image_change(self):
        self.assertEqual(self.run_entrypoint().stdout.strip(), 'image-v1')
        installed = self.data / 'runtime/sub2api'
        self.write_binary(installed, 'web-v2')
        self.write_binary(self.binary, 'image-v3')
        self.assertEqual(self.run_entrypoint().stdout.strip(), 'web-v2')
        self.assertEqual(self.run_entrypoint(persist=False).stdout.strip(), 'image-v3')

    def test_backup_survives_and_can_be_restored(self):
        self.run_entrypoint()
        installed = self.data / 'runtime/sub2api'
        backup = installed.with_suffix('.backup')
        installed.rename(backup)
        self.write_binary(installed, 'web-v2')
        self.assertEqual(self.run_entrypoint().stdout.strip(), 'web-v2')
        backup.replace(installed)
        self.assertEqual(self.run_entrypoint().stdout.strip(), 'image-v1')

    def test_flag_arguments_are_preserved(self):
        output = self.run_entrypoint('--version', 'two words').stdout.splitlines()
        self.assertEqual(output, ['image-v1', '<--version>', '<two words>'])

    def test_custom_command_does_not_bootstrap(self):
        self.assertEqual(self.run_entrypoint('/bin/echo', 'custom').stdout.strip(), 'custom')
        self.assertFalse((self.data / 'runtime').exists())

    def test_disabled_does_not_bootstrap(self):
        self.assertEqual(self.run_entrypoint(persist=False).stdout.strip(), 'image-v1')
        self.assertFalse((self.data / 'runtime').exists())

    def test_broken_persisted_binary_is_not_silently_replaced(self):
        self.run_entrypoint()
        installed = self.data / 'runtime/sub2api'
        original_content = installed.read_bytes()
        installed.chmod(0o644)
        result = self.run_entrypoint(check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(result.stderr.strip())
        self.assertEqual(installed.read_bytes(), original_content)


if __name__ == '__main__':
    unittest.main()
