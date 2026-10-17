"""Focused network-free release notice validation fixtures."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location('generate_notices', Path(__file__).with_name('generate_notices.py'))
notices = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(notices)
LEGAL_TEXT = b'Copyright fixture authors\r\nPermission is hereby granted, free of charge, to use, copy and distribute this software.\r\n' * 2


class NoticeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def npm_fixture(self, name='fixture', version='1.0.0', installed_version=None, license_text=LEGAL_TEXT):
        directory = self.root / 'web/node_modules' / name
        directory.mkdir(parents=True)
        (directory / 'package.json').write_text(json.dumps({
            'name': name, 'version': installed_version or version, 'license': 'MIT'}))
        if license_text is not None:
            (directory / 'LICENSE').write_bytes(license_text)
        (self.root / 'web/package-lock.json').write_text(json.dumps({
            'lockfileVersion': 3, 'packages': {'': {'name': 'web'},
                'node_modules/' + name: {'version': version, 'license': 'MIT', 'integrity': 'sha512-fixture'},
                'node_modules/dev-only': {'version': '1.0.0', 'dev': True}}}))
        return directory

    def test_npm_lock_and_original_bytes(self):
        self.npm_fixture(name='@fixture/scoped')
        result = list(notices.npm_components(self.root))
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0][0]['name'], '@fixture/scoped')
        self.assertEqual(result[0][1], [('LICENSE', LEGAL_TEXT)])

    def test_version_mismatch_fails(self):
        self.npm_fixture(installed_version='2.0.0')
        with self.assertRaisesRegex(notices.NoticeError, 'version mismatch'):
            list(notices.npm_components(self.root))

    def test_missing_or_incomplete_license_fails(self):
        directory = self.npm_fixture(license_text=None)
        for content in (None, b'', b'MIT'):
            if content is not None:
                (directory / 'LICENSE').write_bytes(content)
            with self.subTest(content=content), self.assertRaisesRegex(notices.NoticeError, 'missing or incomplete|empty or binary'):
                list(notices.npm_components(self.root))

    def test_all_applicable_notice_files_and_ancestor_licenses(self):
        (self.root / 'LICENSE').write_bytes(LEGAL_TEXT)
        (self.root / 'LICENSE-3RD-PARTY.md').write_bytes(LEGAL_TEXT)
        (self.root / 'NOTICE').write_bytes(b'Additional attribution\n')
        package = self.root / 'subpackage'
        package.mkdir()
        (package / 'COPYRIGHT').write_bytes(b'Copyright Subpackage Authors\n')
        _, result = notices.component('go', 'fixture', 'v1', self.root, [package])
        self.assertEqual([n for n, _ in result],
                         ['LICENSE', 'LICENSE-3RD-PARTY.md', 'NOTICE', 'subpackage/COPYRIGHT'])

    def test_fallback_requires_exact_version_and_bytes(self):
        self.npm_fixture(name=notices.FALLBACK['name'], version=notices.FALLBACK['version'], license_text=None)
        path = self.root / notices.FALLBACK['file']
        path.parent.mkdir(parents=True)
        path.write_bytes((notices.ROOT / notices.FALLBACK['file']).read_bytes())
        result = list(notices.npm_components(self.root))
        self.assertIn('absent from the 2.3.8 npm tarball', result[0][0]['fallback']['provenance'])
        path.write_bytes(LEGAL_TEXT)
        with self.assertRaisesRegex(notices.NoticeError, 'checksum mismatch'):
            list(notices.npm_components(self.root))
        # A new version must carry its own notice or receive an explicit reviewed fallback.
        manifest = self.root / 'web/node_modules/react-remove-scroll-bar/package.json'
        manifest.write_text(json.dumps({'name': notices.FALLBACK['name'], 'version': '2.3.9', 'license': 'MIT'}))
        lock_path = self.root / 'web/package-lock.json'
        lock = json.loads(lock_path.read_text())
        lock['packages']['node_modules/react-remove-scroll-bar']['version'] = '2.3.9'
        lock_path.write_text(json.dumps(lock))
        with self.assertRaisesRegex(notices.NoticeError, 'missing or incomplete'):
            list(notices.npm_components(self.root))

    def test_go_union_uses_both_entrypoints_and_platforms(self):
        goroot = self.root / 'goroot'
        goroot.mkdir()
        (goroot / 'LICENSE').write_bytes(LEGAL_TEXT)
        module = self.root / 'module'
        module.mkdir()
        (module / 'LICENSE').write_bytes(LEGAL_TEXT)
        calls = []
        def command(args, root, **env):
            calls.append((args, env))
            if args == ['go', 'env', 'GOROOT']:
                return str(goroot)
            if args == ['go', 'env', 'GOVERSION']:
                return 'go1.27.0'
            return json.dumps({'ImportPath': 'example.org/module/' + env['GOOS'],
                               'Dir': str(module), 'Module': {'Path': 'example.org/module',
                               'Version': 'v1.0.0', 'Sum': 'h1:fixture', 'Dir': str(module)}})
        with mock.patch.object(notices, 'command', side_effect=command):
            result = list(notices.go_components(self.root))
        self.assertEqual(result[1][0]['packages'], {'linux': ['example.org/module/linux'],
                                                   'windows': ['example.org/module/windows']})
        lists = [c for c in calls if c[0][1] == 'list']
        self.assertEqual(len(lists), 2)
        self.assertTrue(all(c[0][-2:] == list(notices.ENTRYPOINTS) for c in lists))

    def test_go_command_disables_network_and_implicit_toolchain_download(self):
        result = mock.Mock(returncode=0, stdout=b'{}')
        with mock.patch.object(notices.subprocess, 'run', return_value=result) as run:
            notices.command(['go', 'list'], self.root, GOOS='windows')
        env = run.call_args.kwargs['env']
        for key, value in {'GOPROXY': 'off', 'GOSUMDB': 'off', 'GOTOOLCHAIN': 'local',
                           'CGO_ENABLED': '0', 'GOARCH': 'amd64', 'GOOS': 'windows', 'GOFLAGS': ''}.items():
            self.assertEqual(env[key], value)

    def test_deterministic_output_has_no_host_paths(self):
        for source in ('go.mod', 'go.sum', 'web/package-lock.json', 'deploy/linux/NOTICE',
                       'internal/mapdata/testdata/LZ4-LICENSE.txt'):
            path = self.root / source
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(LEGAL_TEXT)
        item = {'ecosystem': 'go', 'name': 'fixture', 'version': 'v1'}
        with mock.patch.object(notices, 'go_components', side_effect=lambda root: [(dict(item), [('LICENSE', LEGAL_TEXT)])]), \
                mock.patch.object(notices, 'npm_components', return_value=[]):
            first = notices.generate(self.root)
            self.assertEqual(first, notices.generate(self.root))
        self.assertIn(LEGAL_TEXT, first['THIRD_PARTY_NOTICES.txt'])
        self.assertNotIn(str(self.root).encode(), first['release/dependency-inventory.json'])

    def test_check_fails_for_stale_output_without_writing(self):
        path = self.root / 'THIRD_PARTY_NOTICES.txt'
        path.write_bytes(b'old')
        with mock.patch.object(notices, 'ROOT', self.root), \
                mock.patch.object(notices, 'generate', return_value={'THIRD_PARTY_NOTICES.txt': b'new'}), \
                mock.patch('sys.argv', ['generate_notices.py', '--check']), \
                contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(notices.main(), 1)
        self.assertEqual(path.read_bytes(), b'old')


if __name__ == '__main__':
    unittest.main()
