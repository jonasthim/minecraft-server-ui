# Linux installation lifecycle

This installer is in development. Native Ubuntu22.04/24.04 lifecycle qualification is still in progress; see the [implementation ledger](IMPLEMENTATION.md). No stable release is published by these instructions.

The supported layout is local x64 Debian/Ubuntu with systemd. The manager listens on `127.0.0.1:8080` and stores data under `/var/lib/minecraft-ui`. Installation requires root, existing systemd, sudo/runuser and standard account tools. The installer authenticates downloaded inputs before changing services or deployment files. It does not open a firewall port.

## First installation

Independently verify the release-specific bootstrap script and its embedded preparer hash and public keys before running it. Once a qualified release is available, the Linux entry script accepts:

```sh
sudo bash ./minecraft-ui-prepare.sh --version latest --install
```

An exact stable version such as `--version v1.2.3` pins selection. The script resolves `latest` through authenticated release metadata, prepares inputs privately, and passes the resolved exact version to the native installer. `--output /absolute/new/directory` keeps prepared inputs at a chosen new directory; omitting it with `--install` uses temporary private storage. Without `--install`, `--output` is required and the command only prepares inputs.

Development candidates require an independently trusted development public key and explicit `--allow-development`. A trusted native bootstrap can use signed local candidate artifacts for qualification:

```sh
sudo ./minecraft-ui-bootstrap prepare \
  --repository jonasthim/minecraft-server-ui \
  --trust-file /absolute/development-trust.json \
  --version v0.0.1 --artifacts /absolute/candidate \
  --output /absolute/new-prepared --allow-development --install
```

Keep the independently verified bootstrap available for recovery and reinstall after uninstall. A prepared directory does not establish its own trust. Installation rechecks its signed metadata and contents against independently supplied public keys.

## Check, upgrade and recovery

```sh
sudo /usr/local/lib/minecraft-ui/selfupdate install check
sudo /path/to/trusted-bootstrap install recover
```

`check` prints JSON and does not create state, locks or database files. States distinguish absent, installed, uninstalled, retained failed installation, drifted and recovery required. Inspect `coherent` and `pending` as well as `state`; a retained attempted image is not evidence of a running installation.

Rerun the authenticated install command to upgrade. An unchanged healthy installation returns `unchanged` without restarting the manager. Add `--repair` to the bootstrap `prepare --install` command to repair missing files that belong to the recorded installation. Unknown replacements require investigation. Bootstrap trust rotation is separate; operator-configured optional updater trust is preserved.

Install and recovery workers run independently under systemd. If the calling shell disconnects, the worker may still be running. A `pending` result means its final outcome is unknown to that caller. Keep the operation ID, run `install check`, and use `install recover` when needed. Recovery restores the previous executable and deployment files when their recorded identities still match; it never rewinds `manager.db` or worlds. Ambiguous replacements remain intact and are reported for manual repair.

Manager upgrades stop the manager after draining its work. Independent Linux game units remain running. Automatic manager updating ships disabled until the operator supplies its own reviewed updater trust.

## Uninstall and reinstall

Stop all managed game instances before uninstalling:

```sh
sudo /path/to/trusted-bootstrap install uninstall
```

Uninstall removes recorded deployment assets and disables the manager. It retains the service account, data, configuration, trust and installation records. A later authenticated installation can reuse that retained state. A failed first installation also retains account/data and records that distinct state for a checked retry.

`deploy/linux/install.sh` forwards lifecycle commands to the installed helper. It no longer accepts a bare manager executable. The legacy setup script under `deploy/qualification` is restricted to disposable hosted test runners and provides no installer lifecycle evidence.
