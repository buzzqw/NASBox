# Contributing to NASBox

## Before you open a change

- The official client is **NASBox Go** (`client-go/`). The old Python/PyQt6 client
  has been removed; its last version is kept at the Git tag `python-legacy-final`.
- Do not include personal configuration, real hostnames or addresses, SSH keys, logs
  or anything under `server/state/`.
- Keep the shell-script daemon compatible with the bash available on Synology, QNAP
  and generic Linux, and remember that the NAS system `rsync` can be 3.1.2.

## Checks

```bash
cd client-go && go vet ./... && go test ./...      # includes the end-to-end tests
bash -n server/install.sh server/sync-daemon-server.sh server/uninstall.sh
python -m pytest -q                                 # shell-script daemon tests
```

Describe in the pull request the problem solved, the impact on the client and on the
NAS, and how you verified the change.
