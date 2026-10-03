# The HTTP surface

**Generated from the routing table — do not edit.**
Run `go generate ./internal/docs/` after changing routes;
`docs.TestTheGeneratedDocumentsAreCurrent` fails while this file is stale.

151 routes. **15 are reachable without a session**; every one of those also appears in `api.AnonymousAllowlist`, and `Router.register` panics at startup if the two ever disagree.

- **51 hidden** — 404 rather than 403 when unauthorized, so the route's existence is not disclosed (requirements §7.3).
- **19 session-only** — an API token may not use them whatever its scope. Credential management lives here.
- **1 browser-facing** — may redirect a denied navigation instead of answering with JSON (ADR-0012).
- **0 not implemented** — the access class is enforced, the handler answers `501`.

| Column | Meaning |
|---|---|
| `anonymous` | No session required. |
| `enrollment` | A session in `awaiting_mfa` may use it. Nothing else. |
| `authenticated` | Active, MFA-satisfied. Sessions and API tokens. |
| a permission name | That permission, checked before the handler runs. |

## Accounts

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/accounts/requests` | `account.approve` |  |
| GET | `/api/v1/accounts/requests/{id}` | `account.approve` |  |
| POST | `/api/v1/accounts/requests/{id}/approve` | `account.approve` |  |
| POST | `/api/v1/accounts/requests/{id}/deny` | `account.approve` |  |

## Administration

| Method | Path | Requires | Notes |
|---|---|---|---|
| DELETE | `/api/v1/admin/files/{id}` | `library.delete` | hidden |
| DELETE | `/api/v1/admin/indexers/{id}` | `admin.indexers` | hidden |
| DELETE | `/api/v1/admin/media/{id}` | `library.delete` | hidden |
| DELETE | `/api/v1/admin/rootfolders/{id}` | `library.root_folders` | hidden |
| GET | `/api/v1/admin/audit` | `admin.audit` | hidden |
| GET | `/api/v1/admin/audit/summary` | `admin.audit` | hidden |
| GET | `/api/v1/admin/egress` | `admin.network` | hidden |
| GET | `/api/v1/admin/indexers` | `admin.indexers` | hidden |
| GET | `/api/v1/admin/metadata` | `admin.system` | hidden |
| GET | `/api/v1/admin/migrate/sources` | `admin.system` | hidden |
| GET | `/api/v1/admin/notifications` | `admin.system` | hidden |
| GET | `/api/v1/admin/roles` | `admin.users` | hidden |
| GET | `/api/v1/admin/rootfolders` | `library.root_folders` | hidden |
| GET | `/api/v1/admin/subtitles` | `admin.system` | hidden |
| GET | `/api/v1/admin/system/backups` | `admin.system` | hidden |
| GET | `/api/v1/admin/system/logs` | `admin.system` | hidden |
| GET | `/api/v1/admin/system/settings` | `admin.system` | hidden |
| GET | `/api/v1/admin/system/tasks` | `admin.system` | hidden |
| GET | `/api/v1/admin/trash` | `library.delete` | hidden |
| GET | `/api/v1/admin/users` | `admin.users` | hidden |
| PATCH | `/api/v1/admin/egress` | `admin.network` | hidden |
| PATCH | `/api/v1/admin/indexers/{id}` | `admin.indexers` | hidden |
| PATCH | `/api/v1/admin/roles/{id}` | `admin.users` | hidden |
| PATCH | `/api/v1/admin/system/settings` | `admin.system` | hidden |
| PATCH | `/api/v1/admin/users/{id}` | `admin.users` | hidden |
| POST | `/api/v1/admin/egress/leak-test` | `admin.network` | hidden |
| POST | `/api/v1/admin/identify/run` | `admin.system` | hidden |
| POST | `/api/v1/admin/indexers` | `admin.indexers` | hidden |
| POST | `/api/v1/admin/media/{id}/refresh-episodes` | `admin.system` | hidden |
| POST | `/api/v1/admin/metadata/check` | `admin.system` | hidden |
| POST | `/api/v1/admin/migrate/radarr` | `admin.system` | hidden |
| POST | `/api/v1/admin/notifications/test` | `admin.system` | hidden |
| POST | `/api/v1/admin/rootfolders` | `library.root_folders` | hidden |
| POST | `/api/v1/admin/rootfolders/{id}/refresh` | `library.root_folders` | hidden |
| POST | `/api/v1/admin/rootfolders/{id}/scan` | `library.root_folders` | hidden |
| POST | `/api/v1/admin/system/backup` | `admin.system` | hidden |
| POST | `/api/v1/admin/system/restart` | `admin.system` | hidden |
| POST | `/api/v1/admin/system/tasks/{name}/run` | `admin.system` | hidden |
| POST | `/api/v1/admin/trash/purge` | `library.delete` | hidden |
| POST | `/api/v1/admin/trash/restore` | `library.delete` | hidden |
| POST | `/api/v1/admin/users` | `admin.users` | hidden |
| POST | `/api/v1/admin/users/{id}/reset-link` | `admin.users` | hidden |
| POST | `/api/v1/admin/users/{id}/suspend` | `account.suspend` | hidden |
| PUT | `/api/v1/admin/egress/proxy` | `admin.network` | hidden, session only |
| PUT | `/api/v1/admin/metadata/token` | `admin.system` | hidden |
| PUT | `/api/v1/admin/notifications/categories` | `admin.system` | hidden |
| PUT | `/api/v1/admin/notifications/webhook` | `admin.system` | hidden |
| PUT | `/api/v1/admin/quality-profiles/default` | `admin.system` | hidden |
| PUT | `/api/v1/admin/subtitles` | `admin.system` | hidden |
| PUT | `/api/v1/admin/users/{id}/access` | `admin.users` | hidden |

## Albums

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/albums/{id}` | `media.browse` |  |
| POST | `/api/v1/albums/{id}/search` | `acquisition.search` |  |
| PUT | `/api/v1/albums/{id}/monitored` | `library.edit` |  |

## Artwork

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/artwork/poster/{provider}/{id}` | `media.browse` |  |

## Authentication and enrollment

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/auth/mfa/enroll` | enrollment | session only |
| GET | `/api/v1/auth/signup/challenge` | anonymous |  |
| POST | `/api/v1/auth/login` | anonymous |  |
| POST | `/api/v1/auth/login/mfa` | anonymous |  |
| POST | `/api/v1/auth/logout` | enrollment | session only |
| POST | `/api/v1/auth/mfa/enroll` | enrollment | session only |
| POST | `/api/v1/auth/mfa/enroll/confirm` | enrollment | session only |
| POST | `/api/v1/auth/reset/complete` | anonymous |  |
| POST | `/api/v1/auth/reset/initiate` | anonymous |  |
| POST | `/api/v1/auth/signup` | anonymous |  |
| POST | `/api/v1/setup` | anonymous |  |

## Books

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/books/search` | `library.edit` |  |

## Discover

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/discover/{section}` | `media.browse` |  |

## Episodes

| Method | Path | Requires | Notes |
|---|---|---|---|
| POST | `/api/v1/episodes/{id}/search` | `acquisition.search` |  |
| PUT | `/api/v1/episodes/{id}/monitored` | `library.edit` |  |

## Feeds

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/feeds/{token}/calendar.ics` | anonymous |  |
| GET | `/api/v1/feeds/{token}/rss` | anonymous |  |

## Files

| Method | Path | Requires | Notes |
|---|---|---|---|
| DELETE | `/api/v1/files/{id}/position` | `media.browse` |  |
| GET | `/api/v1/files/{id}/convert` | `media.browse` |  |
| GET | `/api/v1/files/{id}/playback` | `media.browse` |  |
| GET | `/api/v1/files/{id}/stream` | `media.browse` |  |
| GET | `/api/v1/files/{id}/subtitles` | `media.browse` |  |
| GET | `/api/v1/files/{id}/subtitles/{sid}` | `media.browse` |  |
| POST | `/api/v1/files/{id}/subtitles/fetch` | `library.edit` |  |
| PUT | `/api/v1/files/{id}/position` | `media.browse` |  |

## Identify

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/identify/pending` | `library.edit` |  |
| GET | `/api/v1/identify/{id}` | `library.edit` |  |
| POST | `/api/v1/identify/{id}/confirm` | `library.edit` |  |
| POST | `/api/v1/identify/{id}/reject` | `library.edit` |  |
| POST | `/api/v1/identify/{id}/reopen` | `library.edit` |  |

## Invites

| Method | Path | Requires | Notes |
|---|---|---|---|
| DELETE | `/api/v1/invites/{id}` | `account.invite` |  |
| GET | `/api/v1/invites` | `account.invite` |  |
| POST | `/api/v1/invites` | `account.invite` |  |

## Issues

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/issues` | `request.submit` |  |
| POST | `/api/v1/issues` | `request.submit` |  |
| POST | `/api/v1/issues/{id}/resolve` | `library.edit` |  |

## Me

| Method | Path | Requires | Notes |
|---|---|---|---|
| DELETE | `/api/v1/me/feeds` | authenticated | session only |
| DELETE | `/api/v1/me/sessions/{id}` | authenticated | session only |
| DELETE | `/api/v1/me/tokens/{id}` | authenticated | session only |
| GET | `/api/v1/me` | authenticated |  |
| GET | `/api/v1/me/feeds` | authenticated | session only |
| GET | `/api/v1/me/sessions` | authenticated | session only |
| GET | `/api/v1/me/tokens` | authenticated | session only |
| PATCH | `/api/v1/me` | authenticated | session only |
| POST | `/api/v1/me/feeds` | authenticated | session only |
| POST | `/api/v1/me/mfa/recovery-codes` | authenticated | session only |
| POST | `/api/v1/me/password` | authenticated | session only |
| POST | `/api/v1/me/tokens` | authenticated | session only |

## Media

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/media` | `media.browse` |  |
| GET | `/api/v1/media/{id}` | `media.browse` |  |
| GET | `/api/v1/media/{id}/albums` | `media.browse` |  |
| GET | `/api/v1/media/{id}/artwork` | `media.browse` |  |
| GET | `/api/v1/media/{id}/children` | `media.browse` |  |
| GET | `/api/v1/media/{id}/original` | `media.download_original` |  |
| POST | `/api/v1/media` | `library.edit` |  |
| POST | `/api/v1/media/{id}/search` | `acquisition.search` |  |
| POST | `/api/v1/media/{id}/seasons/{season}/search` | `acquisition.search` |  |
| PUT | `/api/v1/media/{id}/daily` | `library.edit` |  |
| PUT | `/api/v1/media/{id}/monitored` | `library.edit` |  |
| PUT | `/api/v1/media/{id}/new-seasons` | `library.edit` |  |
| PUT | `/api/v1/media/{id}/quality-profile` | `library.edit` |  |
| PUT | `/api/v1/media/{id}/rating` | `library.edit` |  |
| PUT | `/api/v1/media/{id}/season-folders` | `library.edit` |  |
| PUT | `/api/v1/media/{id}/seasons/{season}/monitored` | `library.edit` |  |

## Metadata

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/metadata/search` | `library.edit` |  |

## Music

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/music/artists` | `library.edit` |  |

## Pages and assets

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/app/` | authenticated | session only |
| GET | `/assets/app/` | authenticated |  |
| GET | `/assets/auth/` | anonymous |  |
| GET | `/enroll` | enrollment | session only |
| GET | `/health/detail` | `admin.system` | hidden |
| GET | `/healthz` | anonymous |  |
| GET | `/login` | anonymous |  |
| GET | `/reset` | anonymous |  |
| GET | `/setup` | anonymous |  |
| GET | `/signup` | anonymous |  |
| GET | `/{$}` | authenticated | session only, browser |

## Quality-profiles

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/quality-profiles` | `acquisition.search` |  |

## Queue

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/queue` | `acquisition.queue` |  |
| GET | `/api/v1/queue/{id}/history` | `acquisition.queue` |  |
| POST | `/api/v1/queue/{id}/remove` | `acquisition.queue` |  |

## Releases

| Method | Path | Requires | Notes |
|---|---|---|---|
| POST | `/api/v1/releases/grab` | `acquisition.queue` |  |
| POST | `/api/v1/releases/search` | `acquisition.search` |  |

## Requests

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/requests` | `request.submit` |  |
| POST | `/api/v1/requests` | `request.submit` |  |
| POST | `/api/v1/requests/{id}/approve` | `request.approve` |  |
| POST | `/api/v1/requests/{id}/deny` | `request.approve` |  |
| POST | `/api/v1/requests/{id}/item` | `request.approve` |  |

## Roles

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/roles` | `account.approve` |  |

## Rootfolders

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/rootfolders` | `media.browse` |  |

## Search

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/search` | `media.browse` |  |

## Subtitles

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/subtitles/languages` | `library.edit` |  |

## Wanted

| Method | Path | Requires | Notes |
|---|---|---|---|
| GET | `/api/v1/wanted` | `media.browse` |  |

