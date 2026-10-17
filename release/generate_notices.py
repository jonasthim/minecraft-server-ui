#!/usr/bin/env python3
"""Generate release notices from local, locked build inputs; never fetch dependencies."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
TARGETS = ('linux', 'windows')
ENTRYPOINTS = ('./cmd/minecraft-ui', './cmd/minecraft-ui-update')
LEGAL = re.compile(r'^(?:LICEN[CS]E|COPYING|COPYRIGHT|NOTICE|UNLICENSE|PATENTS)(?:$|[.\-_])', re.I)
LICENSE = re.compile(r'^(?:LICEN[CS]E|COPYING|UNLICENSE)(?:$|[.\-_])', re.I)
FALLBACK = {
    'name': 'react-remove-scroll-bar',
    'version': '2.3.8',
    'file': 'release/licenses/react-remove-scroll-bar/LICENSE',
    'sha256': 'a79aae0c0f21990d9d963bb3c5a79cdcea9a46f8523ba55c58d7fe776b6ebc84',
    'url': 'https://raw.githubusercontent.com/theKashey/react-remove-scroll-bar/7301c160fda44cb8cf2b9fdfde61efad35736196/LICENSE',
    'provenance': 'Later upstream notice added 2025-05-21 at commit '
                  '7301c160fda44cb8cf2b9fdfde61efad35736196; absent from the 2.3.8 npm tarball '
                  'and its gitHead b3b1287aad81def2e2ae707274b74531b61ddbaf. '
                  'The locked package declares MIT. This is not a notice supplied in that tarball.',
}


class NoticeError(Exception):
    pass


def digest(data):
    return hashlib.sha256(data).hexdigest()


def command(args, root, **extra):
    env = dict(os.environ, GOPROXY='off', GOSUMDB='off', GOTOOLCHAIN='local',
               GOFLAGS='', CGO_ENABLED='0', GOARCH='amd64', GOAMD64='v1',
               GOWORK='off', GOEXPERIMENT='', **extra)
    result = subprocess.run(args, cwd=root, env=env, capture_output=True, check=False)
    if result.returncode:
        # Tool output may contain private cache paths; keep the public error bounded.
        raise NoticeError(f'{args[0]} {args[1]} failed; provision locked dependencies/toolchain locally')
    return result.stdout.decode('utf-8')


def json_stream(data):
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(data):
        if data[offset].isspace():
            offset += 1
            continue
        item, offset = decoder.raw_decode(data, offset)
        yield item


def legal_files(directory):
    return sorted((p for p in directory.iterdir() if p.is_file() and LEGAL.match(p.name)),
                  key=lambda p: p.name)


def read_notice(path):
    data = path.read_bytes()
    if not data.strip() or b'\x00' in data:
        raise NoticeError(f'empty or binary legal text: {path.name}')
    try:
        data.decode('utf-8')
    except UnicodeDecodeError as exc:
        raise NoticeError(f'legal text is not UTF-8: {path.name}') from exc
    return data


def component(kind, name, version, directory, package_dirs=()):
    """Root notices plus legal notices inherited by linked subpackages."""
    directory = directory.resolve()
    files = set(legal_files(directory))
    for package_dir in package_dirs:
        current = Path(package_dir).resolve()
        if not current.is_relative_to(directory):
            raise NoticeError(f'package outside dependency root: {name}')
        while current != directory:
            files.update(legal_files(current))
            current = current.parent
    if not any(LICENSE.match(p.name) and len(read_notice(p).strip()) >= 100 for p in files):
        raise NoticeError(f'missing or incomplete license: {name}@{version}')
    notices = [(p.relative_to(directory).as_posix(), read_notice(p)) for p in sorted(files)]
    return {'ecosystem': kind, 'name': name, 'version': version}, notices


def go_components(root):
    goroot = Path(command(['go', 'env', 'GOROOT'], root).strip())
    version = command(['go', 'env', 'GOVERSION'], root).strip()
    modules = {}
    standard_dirs = set()
    standard_packages = {target: [] for target in TARGETS}
    for target in TARGETS:
        packages = list(json_stream(command(
            ['go', 'list', '-mod=readonly', '-deps', '-json', *ENTRYPOINTS], root, GOOS=target)))
        if not packages:
            raise NoticeError(f'empty Go inventory: {target}')
        for package in packages:
            if package.get('Error') or package.get('DepsErrors'):
                raise NoticeError(f'incomplete Go inventory: {target}')
            if package.get('Standard'):
                standard_dirs.add(package['Dir'])
                standard_packages[target].append(package['ImportPath'])
                continue
            module = package.get('Module')
            if not module:
                raise NoticeError('Go package has no module provenance')
            if module.get('Main'):
                continue
            if module.get('Replace') or not module.get('Version') or not module.get('Sum'):
                raise NoticeError(f'unpinned or replaced Go module: {module["Path"]}')
            key = (module['Path'], module['Version'])
            entry = modules.setdefault(key, {'module': module, 'dirs': set(),
                                              'packages': {t: [] for t in TARGETS}})
            if entry['module']['Sum'] != module['Sum']:
                raise NoticeError('conflicting module checksums')
            entry['dirs'].add(package['Dir'])
            entry['packages'][target].append(package['ImportPath'])
    item, notices = component('go-toolchain', 'Go', version, goroot, standard_dirs)
    item['packages'] = {t: sorted(set(p)) for t, p in standard_packages.items()}
    yield item, notices
    for (name, version), entry in sorted(modules.items()):
        item, notices = component('go', name, version, Path(entry['module']['Dir']), entry['dirs'])
        item['module_sum'] = entry['module']['Sum']
        item['packages'] = {t: sorted(set(p)) for t, p in entry['packages'].items()}
        yield item, notices


def npm_components(root):
    lock = json.loads((root / 'web/package-lock.json').read_text())
    if lock.get('lockfileVersion') != 3:
        raise NoticeError('expected npm lockfileVersion 3')
    for location, locked in sorted(lock['packages'].items()):
        if not location or locked.get('dev'):
            continue
        parts = PurePosixPath(location).parts
        if ('..' in parts or not parts or parts[0] != 'node_modules'
                or PurePosixPath(location).is_absolute() or '\\' in location or locked.get('link')):
            raise NoticeError('unsafe or linked npm package location')
        directory = root / 'web' / location
        # Derive identity from each node_modules segment, including nested/scoped packages.
        index = len(parts) - 1 - list(reversed(parts)).index('node_modules')
        name = '/'.join(parts[index + 1:])
        if not name or not locked.get('version') or not locked.get('integrity'):
            raise NoticeError(f'unpinned npm package: {location}')
        manifest = json.loads((directory / 'package.json').read_text())
        if manifest.get('name') != name or manifest.get('version') != locked['version']:
            raise NoticeError(f'installed npm identity/version mismatch: {name}')
        if manifest.get('license') != locked.get('license'):
            raise NoticeError(f'installed npm license declaration mismatch: {name}')
        if name == FALLBACK['name'] and locked['version'] == FALLBACK['version']:
            if manifest.get('license') != 'MIT':
                raise NoticeError('fallback requires locked MIT declaration')
            path = root / FALLBACK['file']
            data = read_notice(path)
            if digest(data) != FALLBACK['sha256']:
                raise NoticeError('fallback license checksum mismatch')
            # Still retain every legal file supplied in the installed package.
            notices = [(p.name, read_notice(p)) for p in legal_files(directory)]
            notices.append((FALLBACK['file'], data))
            item = {'ecosystem': 'npm', 'name': name, 'version': locked['version'],
                    'fallback': FALLBACK}
        else:
            item, notices = component('npm', name, locked['version'], directory)
        item.update(location=location, integrity=locked['integrity'], license=locked.get('license'))
        yield item, notices


def generate(root):
    components = list(go_components(root)) + list(npm_components(root))
    # Preserve copied-source notices, including the test fixture conservatively.
    for source in ('deploy/linux/NOTICE', 'internal/mapdata/testdata/LZ4-LICENSE.txt'):
        components.append(({'ecosystem': 'copied-notice', 'name': source,
                            'version': 'repository'}, [(source, read_notice(root / source))]))
    components.sort(key=lambda c: (c[0]['ecosystem'], c[0]['name'], c[0]['version'],
                                   c[0].get('location', '')))
    inventory = {'schema_version': 1,
                 'basis': {'go': {'entrypoints': list(ENTRYPOINTS), 'goos': list(TARGETS),
                                  'goarch': 'amd64', 'cgo_enabled': False, 'build_tags': []},
                           'npm': 'All package-lock v3 entries without dev=true; conservative bundled UI superset',
                           'notices': 'All root and linked Go package-ancestor legal files; root npm legal files; explicit copied notices'},
                 'inputs': {p: digest((root / p).read_bytes()) for p in
                            ('go.mod', 'go.sum', 'web/package-lock.json')},
                 'components': []}
    output = bytearray(b'THIRD PARTY NOTICES - Minecraft Server UI\n\n'
                       b'Generated by release/generate_notices.py; see docs/third-party-notices.md.\n'
                       b'Go: Linux/Windows amd64 manager and updater linked package union, CGO disabled.\n'
                       b'UI: locked non-dev npm packages (conservative superset of bundled code).\n'
                       b'Includes Go toolchain and preserved copied-source notices.\n'
                       b'Project license is distributed separately as LICENSE.\n')
    for item, notices in components:
        output.extend(f'\n{"=" * 78}\n{item["ecosystem"]}: {item["name"]} @ {item["version"]}\n'.encode())
        if 'fallback' in item:
            output.extend((item['fallback']['provenance'] + '\nSource: ' + item['fallback']['url'] + '\n').encode())
        item['notices'] = []
        for source, data in notices:
            item['notices'].append({'file': source, 'sha256': digest(data), 'bytes': len(data)})
            output.extend(f'\n--- {source} ---\n'.encode())
            output.extend(data)  # Preserve the original legal text bytes, including line endings.
            if not data.endswith(b'\n'):
                output.extend(b'\n')
        inventory['components'].append(item)
    return {'THIRD_PARTY_NOTICES.txt': bytes(output),
            'release/dependency-inventory.json': (json.dumps(inventory, indent=2, ensure_ascii=True) + '\n').encode()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='fail if checked-in artifacts differ')
    args = parser.parse_args()
    try:
        artifacts = generate(ROOT)
        for location, data in artifacts.items():
            path = ROOT / location
            if args.check:
                if not path.exists() or path.read_bytes() != data:
                    raise NoticeError(f'stale generated artifact: {location}; run make notices')
            else:
                path.write_bytes(data)
    except (NoticeError, OSError, ValueError, KeyError) as exc:
        print(f'notice generation failed: {exc}', file=sys.stderr)
        return 1
    print('Release notice artifacts ' + ('verified' if args.check else 'generated') + ' (offline).')
    return 0


if __name__ == '__main__':
    sys.exit(main())
