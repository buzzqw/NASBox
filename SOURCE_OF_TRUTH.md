# Source of Truth

This is the canonical source layout for NASBox.

- Git remote: `https://github.com/buzzqw/NASBox.git`
- Canonical NAS checkout: `/volume1/Varie/sync-daemon`
- NAS daemon: `/volume1/Varie/sync-daemon/server/sync-daemon-server.sh`
- Synced data root: `/volume1/NASBox`
- Client update bundle: `/volume1/NASBox/.nasbox-client-update`

The daemon package and the synced data are intentionally separate. `SHARE_ROOT`
must remain `/volume1/NASBox`; moving the package must never move that folder.

Make source changes in the canonical checkout, commit them, and push them to
the GitHub remote. The client copies under `~/NASBox/sync-daemon` are runtime
installations only: they must not contain `.git` and must not be edited as
source repositories. Their synchronization excludes `sync-daemon/` and
`.nasbox-client-update/`.

The NAS-only `server.conf`, `server/state/`, client configuration, logs, and
update bundles are deployment data and must not be committed.
