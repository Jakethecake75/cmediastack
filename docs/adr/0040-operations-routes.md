# ADR-0040: The operations routes — detailed health, recent logs, and settings that are read, not written

**Status:** accepted
**Date:** 2026-09-29
**Related:** [ADR-0013](0013-egress-guard-design.md),
[ADR-0029](0029-encrypted-verified-backups.md),
[ADR-0031](0031-reading-the-audit-log.md), docs/RUNBOOK.md §7

## The problem

Four administration routes have answered 501 since Phase 1:

- `GET /health/detail`
- `GET /api/v1/admin/system/logs`
- `GET /api/v1/admin/system/settings`
- `PATCH /api/v1/admin/system/settings`

An operator who wants to know whether the instance is well, what it has been
saying, or what it is configured to do has to read the host: the container's
logs, the config file, the tasks screen, the backups screen, one at a time.

## Decisions

### 1. `GET /health/detail` is every check in one answer

`admin.system`, hidden like every administration route. The unauthenticated
`/healthz` on the management listener stays what it is — `ok` and nothing else.
The detailed report is a list of named checks, each `ok`, `warn` or `fail`, with
a sentence, and an overall state that is the worst of them:

| Check | `fail` | `warn` |
|---|---|---|
| database | cannot be read | — |
| egress | enforced and the tunnel is not verified | — |
| tasks | — | a task whose last run failed, named |
| backups | — | none yet, or the newest older than twice the interval |
| storage | a root folder that cannot be read | under 5 % or 10 GiB free |
| media parser sandbox | — | not available (parsing runs unjailed, ADR-0020) |
| metadata | — | no provider configured |

Plus the version, the schema version and the uptime. Nothing in it is a secret;
it is administrator-only because it describes the instance's weak points.

### 2. `GET /api/v1/admin/system/logs` is the process's recent log records

The last 1 000 records, kept in memory as the process writes them, **after
redaction**: the ring sits behind the handler that masks tokens, passwords and
keys, so it holds exactly what the log output holds. It is filtered by level
and text, newest first, at most 500 an answer. It is not the log's history:
records start at the process's start and are gone when it stops. The host's log
is the history. `admin.system`, and reading it is not audited, as reading the
audit log is not (ADR-0031).

### 3. Settings are read here and changed in the file

`GET /api/v1/admin/system/settings` is the effective configuration, as the file
spells it after the environment is applied. The file holds no secret: the master
key is named by the environment variable that holds it (`master_key_env`), and
the credentials the instance stores are sealed in the database, never here.

`PATCH /api/v1/admin/system/settings` answers **409, permanently**, and says
where the setting lives. It is the decision the egress policy made: the
configuration is reviewed where it is written, and a runtime switch for
automatic acquisition, registration or the egress mode would be the first
thing a stolen administrator session reached for. Settings that are runtime
state — the default profile, the metadata key, notifications — have their own
routes already.

## Rejected alternatives

**Writing settings back to the file from the web.** It puts the web process in
charge of its own configuration file, and needs write access to it.

**Serving the host's log files.** They are wherever the container runtime puts
them, and a route that reads a path is what ADR-0015 spent an increment removing.

**Auditing log reads.** The audit log would then log its own readers' reading of
other logs. Reading is not an effect.

## Known limitations

- The log ring is lost at restart, and holds 1 000 records: a busy hour can push
  a morning's warning out.
- Free space is measured when the report is asked for, per root, not watched.

## Verification

| Claim | Test |
|---|---|
| The report names each check, and the overall state is the worst | `api.TestHealthDetailSaysWhatIsWrong` |
| Logs are the recent records, redacted, filtered, newest first, bounded | `logging.TestTheRingKeepsRecentRedactedRecords`, `api.TestRecentLogsAreReadable` |
| Settings are read, and never written over HTTP | `api.TestSettingsAreReadNotWritten` |
