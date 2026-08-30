# NASBox operational and load tests

The automated suite runs entirely in disposable directories. It does not use
the production NAS or any personal client configuration.

## Local protocol load test

Run a small smoke profile:

```bash
PYTHONPATH=client python3 tools/nasbox_load_test.py --files 1000 --size 4096
```

Run a larger metadata profile:

```bash
PYTHONPATH=client python3 tools/nasbox_load_test.py --files 10000 --size 16384
```

The harness measures journal append, manifest export, revision notification and
the read-only metrics command. It prints JSON suitable for archiving in a CI
artifact. It does not measure real SSH or rsync throughput.

## Two-client operational test

Use a non-production NAS share and two disposable client configurations. The
following matrix is the minimum release-candidate checklist:

| Scenario | Expected result |
|---|---|
| Edit on client A | File reaches NAS and client B |
| Client B offline | B catches up after reconnect |
| Network loss during transfer | Retry without partial live file |
| Same file edited on both clients | Explicit recoverable conflict |
| Rename from Browse | Move and source tombstone propagate |
| Delete during another upload | No stale delete overwrites newer content |
| Client crash during upload | Lock releases and retry is safe |
| NAS restart during commit | Journal and manifest converge after recovery |
| Disk nearly full | Clear block before destructive operation |
| Names with tab/newline/Unicode | Exact path preserved |

After every scenario compare SHA-256 snapshots of client A, client B and the
NAS, excluding `.sync-trash`, `.nasbox-staging` and runtime state. Also verify
that the journal and manifest describe the same committed state.

## NAS load profiles

Run these on a lab NAS only, never on `/volume1/NASBox` in production:

| Profile | Workload |
|---|---|
| Idle | Monitor open for 1 hour, no file changes |
| Small | 1,000 small files and 10 edits/minute |
| Medium | 10,000 files and 100 edits/minute |
| Import | 100,000 new files from one client |
| Churn | 1-10% of files modified repeatedly |
| Large files | 1, 5, 20 and 100 GB files with interruptions |
| Concurrency | Two to four clients editing simultaneously |
| History | Journal and trash near retention/size limits |

Collect at one-minute intervals:

- client-to-NAS and NAS-to-client latency;
- lock wait and lock duration;
- upload/download throughput;
- CPU, memory, swap and iowait;
- disk read/write counters;
- network counters;
- journal, manifest, staging and trash sizes;
- pending count and oldest pending age;
- SSH and rsync process count.

The monitor must not run `find` or `du` in its fast path. Its idle test passes
when the NAS shows no recurring scan of the synchronized tree and no material
increase in CPU or disk activity compared with the daemon sleeping alone.

## Soak test

The release candidate should run for 72 hours with two or more clients, random
create/modify/delete/rename operations, periodic restarts and injected network
interruptions. A release fails on any unrecoverable data loss, manifest drift,
partial live file, orphaned lease, unbounded pending queue or unbounded staging
growth.
