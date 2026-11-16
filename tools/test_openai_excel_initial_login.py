import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock, patch

from tools import openai_oauth_reauth_worker as worker
from tools import openai_excel_oauth_adapter as adapter
from tools.test_openai_excel_oauth_adapter import token


class ExcelInitialLoginTests(unittest.TestCase):
    def api(self):
        return SimpleNamespace(config=SimpleNamespace(worker_id='worker', tosub2_root=None),
                               _post=Mock(return_value={'active': True, 'status': 'succeeded'}))

    def test_callbacks_never_use_the_account_mutation_namespace(self):
        base = self.api()
        api = worker.InitialExcelAPI(base)
        api.credentials(123, {'access_token': 'synthetic'}, {'unexpected': 'ignored'})
        base._post.assert_called_with('/api/v1/internal/openai-reauth/123/initial-excel/result',
                                      {'worker_id': 'worker', 'credentials': {'access_token': 'synthetic'}})
        api.fail(123)
        base._post.assert_called_with('/api/v1/internal/openai-reauth/123/initial-excel/fail',
                                      {'worker_id': 'worker', 'error_code': 'worker_failed', 'error_stage': 'starting'})

    def test_canceled_runner_is_terminated_without_returning_credentials(self):
        api = worker.InitialExcelAPI(self.api())
        api.active = Mock(side_effect=[True, False])
        started = time.monotonic()
        with self.assertRaisesRegex(worker.WorkerError, 'canceled'):
            worker.run_initial_excel_process(api, 123, [sys.executable, '-c', 'import time; time.sleep(30)'],
                                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=20, check=False)
        self.assertLess(time.monotonic() - started, 8)

    def test_stale_job_never_starts_the_protocol(self):
        api = worker.InitialExcelAPI(self.api())
        api.active = Mock(return_value=False)
        with patch.object(worker.subprocess, 'Popen') as spawn, self.assertRaises(worker.WorkerError):
            worker.run_initial_excel_process(api, 123, ['unused'], timeout=20)
        spawn.assert_not_called()

    def test_reuses_excel_password_protocol_without_an_existing_account(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root/'src').mkdir()
            script = root/'src/protocol-login.mjs'
            script.write_text('// synthetic; never executed')
            base = self.api()
            base.config.tosub2_root = root
            api = worker.InitialExcelAPI(base)
            credentials = {'client_id': adapter.CLIENT_ID, 'chatgpt_account_id': 'workspace', 'refresh_token': 'synthetic',
                           'access_token': token({'client_id': adapter.CLIENT_ID, 'exp': time.time()+3600,
                                                  'https://api.openai.com/auth': {'chatgpt_account_id': 'workspace'}}),
                           'id_token': token({'aud': adapter.CLIENT_ID, 'email': 'bps@example.com'})}
            claim = {'task_id': 123, 'account_id': 0, 'oauth_profile': 'excel', 'engine': 'local_worker',
                     'login_email': 'bps@example.com', 'password': 'PRIVATE_PASSWORD', 'totp_secret': 'PRIVATE_TOTP',
                     'proxy_url': 'http://127.0.0.1:1234'}
            def run(_api, task_id, command, **options):
                self.assertIs(_api, api)
                self.assertEqual(task_id, 123)
                self.assertEqual(options['env']['CHATGPT_LOGIN_PASSWORD'], 'PRIVATE_PASSWORD')
                self.assertEqual(command[command.index('--proxy')+1], claim['proxy_url'])
                self.assertNotIn('PRIVATE_PASSWORD', ' '.join(command))
                Path(command[command.index('--sub2api-out')+1]).write_text(json.dumps({'accounts': [{'credentials': credentials}]}))
                return subprocess.CompletedProcess(command, 0, '', '')
            with patch.object(adapter, 'prepare_excel_runtime', return_value=script) as prepare, patch.object(
                worker, 'run_initial_excel_process', side_effect=run
            ), patch.object(worker.shutil, 'which', return_value='/synthetic/node'):
                worker.process_password_claim(api, claim)
            prepare.assert_called_once()
            self.assertTrue(any(call.args[0].endswith('/initial-excel/result') for call in base._post.call_args_list))

    def test_failed_initial_job_does_not_mutate_or_report_to_an_account_task(self):
        api = self.api()
        api.claim = Mock(return_value=None)
        api.claim_initial_excel = Mock(return_value={'task_id':123, 'account_id':0, 'oauth_profile':'excel'})
        api.fail = Mock()
        with patch.object(worker, 'process_password_claim', side_effect=RuntimeError('PRIVATE_PASSWORD')), self.assertLogs(worker.LOGGER) as logs:
            self.assertTrue(worker.run_once(api, None))
        api.fail.assert_not_called()
        self.assertNotIn('PRIVATE_PASSWORD', str(logs.output))
        self.assertTrue(api._post.call_args.args[0].endswith('/initial-excel/fail'))

    def test_older_servers_can_still_process_session_export_jobs(self):
        api = self.api()
        api.claim = Mock(return_value=None)
        api.claim_initial_excel = Mock(side_effect=worker.WorkerError('HTTP 404'))
        api.claim_session_export = Mock(return_value={'task_id':1})
        with patch.object(worker, 'process_session_export_claim') as process:
            self.assertTrue(worker.run_once(api, None))
        process.assert_called_once()

    def test_failure_details_never_include_raw_exception_text(self):
        for error in (RuntimeError('PRIVATE_PASSWORD'), worker.WorkerError('PRIVATE_TOKEN'),
                      worker.WorkerError('Excel password/TOTP login failed (stage=private, reason=private, exit=1)')):
            code, stage = worker.initial_excel_failure(error, 'PRIVATE_STAGE')
            self.assertEqual((code, stage), ('worker_failed', 'starting'))
        self.assertEqual(worker.initial_excel_failure(worker.WorkerError('password/TOTP protocol timed out'),
                                                    'protocol_connecting'), ('timeout', 'protocol_connecting'))

    @unittest.skipUnless(os.getenv('EXCEL_TEST_TOSUB2_ROOT'), 'set EXCEL_TEST_TOSUB2_ROOT to the pinned dependency')
    def test_real_cli_reports_initial_login_results_without_mocking_login_helpers(self):
        prepare = adapter.prepare_excel_runtime
        def offline_prepare(root, temporary):
            script = prepare(root, temporary)
            shutil.copyfile(Path(__file__).parent/'fixtures/excel-http-transport.mjs', script.with_name('tls-transport.mjs'))
            return script
        for mode, code in [('success', None), ('password', 'password_rejected'), ('totp', 'totp_rejected'),
                           ('additional', 'additional_verification'), ('transport', 'transport_error'),
                           ('security', 'security_check'), ('wrong-state', 'callback_state_mismatch')]:
            with self.subTest(mode=mode):
                api = self.api()
                api.config.tosub2_root = Path(os.environ['EXCEL_TEST_TOSUB2_ROOT'])
                api.claim = Mock(return_value=None)
                api.claim_initial_excel = Mock(return_value={
                    'task_id': 123, 'account_id': 0, 'oauth_profile': 'excel', 'engine': 'local_worker',
                    'login_email': 'test@example.test', 'password': 'synthetic-password', 'totp_secret': 'JBSWY3DPEHPK3PXP'})
                with patch.object(adapter, 'prepare_excel_runtime', side_effect=offline_prepare), patch.dict(
                        os.environ, {'EXCEL_TEST_MODE': mode}), self.assertLogs(worker.LOGGER, level='INFO') as logs:
                    self.assertTrue(worker.run_once(api, None))
                self.assertNotIn('PRIVATE_TOKEN', str(logs.output))
                self.assertNotIn('synthetic-password', str(logs.output))
                callbacks = [call for call in api._post.call_args_list if call.args[0].endswith(('/result', '/fail'))]
                self.assertEqual(len(callbacks), 1)
                if code is None:
                    self.assertTrue(callbacks[0].args[0].endswith('/result'))
                    adapter.validate_excel_credentials(callbacks[0].args[1]['credentials'], 'test@example.test')
                else:
                    self.assertTrue(callbacks[0].args[0].endswith('/fail'))
                    self.assertEqual(callbacks[0].args[1]['error_code'], code)
                    self.assertIn(callbacks[0].args[1]['error_stage'], worker.EXCEL_FAILURE_STAGES)
