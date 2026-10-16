#!/usr/bin/env python3
"""Fail-closed machine gate; human review of linked evidence remains mandatory."""
import argparse
import json
import pathlib
import re
import sys


def check(root: pathlib.Path, version: str) -> list[str]:
    document = json.loads((root / 'release/qualification.json').read_text())
    failures = []
    if not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', version):
        failures.append('A strict stable vMAJOR.MINOR.PATCH is required.')
    if document.get('format') != 1 or document.get('stable_publish_enabled') is not True:
        failures.append('Stable publishing has not been enabled by the release owner.')
    if document.get('product_version') != version:
        failures.append('The qualified product version does not match the requested release.')
    if not document.get('release_repository') or not document.get('trusted_public_keys'):
        failures.append('Production release repository and public verification keys are unconfigured.')
    required = {
        'independent_whole_branch_review',
        'linux_debian_fresh_database_admin_install_start_client_backup_restore',
        'linux_ubuntu_fresh_database_admin_install_start_client_backup_restore',
        'windows_native_fresh_database_admin_install_start_client_backup_restore',
        'linux_manager_update_health_rollback_recovery',
        'windows_manager_update_health_rollback_recovery',
        'minecraft_1_12_legacy', 'minecraft_1_16_packing',
        'minecraft_1_18_negative_y', 'minecraft_1_20_5_mixed_codecs',
        'minecraft_1_21_9_authenticated_msmp', 'minecraft_26_1_folders',
        'minecraft_26_3_logs', 'archive_traversal_hash_failure',
        'online_save_lease_watchdog_crash_recovery',
        'instance_update_recovery_competing_writes', 'roles_audit_redaction',
        'mobile_390_keyboard_first_use', 'dependency_license_notice_review',
    }
    evidence = document.get('required_evidence', {})
    for key in sorted(required):
        item = evidence.get(key)
        if not isinstance(item, dict) or item.get('result') != 'passed' or item.get('version') != version:
            failures.append(f'Missing reviewed evidence: {key}.')
            continue
        path = item.get('report')
        if not isinstance(path, str) or not path.startswith('docs/reports/') or '..' in pathlib.PurePosixPath(path).parts:
            failures.append(f'Invalid evidence path: {key}.')
            continue
        target = root / path
        if any(part.is_symlink() for part in [target, *target.parents]) or not target.is_file() or target.stat().st_size == 0:
            failures.append(f'Unavailable evidence file: {key}.')
    return failures


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('version')
    arguments = parser.parse_args()
    root = pathlib.Path(__file__).resolve().parent.parent
    errors = check(root, arguments.version)
    if errors:
        print('\n'.join(errors), file=sys.stderr)
        sys.exit(1)
    print('Machine evidence gate passed; explicit publication authorization is still required.')
