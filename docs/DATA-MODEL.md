# The data model

**Generated from the schema the migrations produce — do not edit.**
Run `go generate ./internal/docs/` after adding a migration;
`docs.TestTheGeneratedDocumentsAreCurrent` fails while this file is stale.

37 migrations applied ([1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37]). SQLite with `foreign_keys=ON` and WAL (ADR-0004) — the declared references below are enforced, not decorative.

## `account_request`

*Migration 0001.*

An account request is NOT a user. It holds no session, no token and no permission, and it cannot authenticate. The password is Argon2id-hashed at submission time and never stored reversibly.

This table is a small PII store (email, source IP, user agent) and is purged on expiry rather than archived.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `username` | text | **no** | — | — |
| `email` | text | **no** | — | — |
| `password_hash` | text | **no** | — | — |
| `note` | text | yes | — | — |
| `source_ip` | text | yes | — | — |
| `user_agent` | text | yes | — | — |
| `invite_id` | integer | yes | — | → `invite.id` |
| `state` | text | **no** | — | — |
| `created_at` | text | **no** | — | — |
| `expires_at` | text | **no** | — | — |
| `decided_at` | text | yes | — | — |
| `decided_by_user_id` | integer | yes | — | → `app_user.id` |
| `deny_reason` | text | yes | — | — |

Indexes: `idx_account_request_expires`, `idx_account_request_pending_email`, `idx_account_request_state`

## `acquire_state`

*Migration 0018.*

What automatic acquisition last did about each wanted item: when it last searched for it, what came of it, and when it is due again. One row per episode or per film, never both; gone with the episode or film.

It records searches, not grabs: what was grabbed is the download queue's, which is also the blocklist (ADR-0030, decision 3).

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `episode_id` | integer | yes | — | → `episode.id (cascade)` |
| `item_id` | integer | yes | — | → `media_item.id (cascade)` |
| `album_id` | integer | yes | — | → `album.id (cascade)` |
| `searched_at` | text | yes | — | — |
| `next_at` | text | yes | — | — |
| `fruitless` | integer | **no** | `0` | — |
| `outcome` | text | yes | — | — |
| `detail` | text | **no** | `''` | — |

Indexes: `idx_acquire_state_album`, `idx_acquire_state_episode`, `idx_acquire_state_item`

## `album`

*Migration 0026.*

An album is a MusicBrainz release group of an artist: an Album or an EP. Its track list is that of one official release, the earliest, fetched when it is first needed (tracks_refreshed_at NULL until then).

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `item_id` | integer | **no** | — | → `media_item.id (cascade)` |
| `musicbrainz_id` | text | **no** | — | — |
| `release_id` | text | yes | — | — |
| `title` | text | **no** | — | — |
| `album_type` | text | **no** | — | — |
| `released_at` | text | yes | — | — |
| `monitored` | integer | **no** | `1` | — |
| `tracks_refreshed_at` | text | yes | — | — |
| `created_at` | text | **no** | — | — |
| `updated_at` | text | **no** | — | — |

Indexes: `idx_album_item`

## `api_token`

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `user_id` | integer | **no** | — | → `app_user.id (cascade)` |
| `name` | text | **no** | — | — |
| `token_hash` | text | **no** | — | — |
| `permissions_json` | text | **no** | `'[]'` | — |
| `created_at` | text | **no** | — | — |
| `expires_at` | text | yes | — | — |
| `last_used_at` | text | yes | — | — |
| `revoked_at` | text | yes | — | — |

Indexes: `idx_api_token_user`

## `app_user`

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `username` | text | **no** | — | — |
| `email` | text | **no** | — | — |
| `password_hash` | text | **no** | — | — |
| `state` | text | **no** | — | — |
| `role_id` | integer | **no** | — | → `role.id` |
| `totp_secret_enc` | blob | yes | — | — |
| `totp_enrolled_at` | text | yes | — | — |
| `rating_ceiling` | integer | **no** | `0` | — |
| `created_at` | text | **no** | — | — |
| `updated_at` | text | **no** | — | — |
| `approved_by_user_id` | integer | yes | — | → `app_user.id` |
| `last_login_at` | text | yes | — | — |
| `totp_last_counter` | integer | **no** | `0` | — |
| `all_libraries` | integer | **no** | `1` | — |

Indexes: `idx_app_user_state`

## `audit_event`

*Migration 0001.*

Append-only. No repository method issues UPDATE or DELETE against this table, and a test asserts that.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `occurred_at` | text | **no** | — | — |
| `actor_user_id` | integer | yes | — | — |
| `actor_label` | text | **no** | — | — |
| `action` | text | **no** | — | — |
| `outcome` | text | **no** | — | — |
| `target_kind` | text | yes | — | — |
| `target_id` | text | yes | — | — |
| `source_ip` | text | yes | — | — |
| `user_agent` | text | yes | — | — |
| `detail` | text | yes | — | — |
| `before_json` | text | yes | — | — |
| `after_json` | text | yes | — | — |

Indexes: `idx_audit_action`, `idx_audit_actor`, `idx_audit_occurred`

## `auth_attempt`

*Migration 0001.*

Throttling ledger for login and signup. Keyed by "ip:<addr>" or "user:<id>".

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `key` | text | **no** | — | — |
| `kind` | text | **no** | — | — |
| `occurred_at` | text | **no** | — | — |
| `success` | integer | **no** | — | — |

Indexes: `idx_auth_attempt_key`

## `download_queue`

*Migration 0005.*

The download queue, persisted.

Without this table a restart orphans every partial transfer: the bytes stay on disk under the data directory, named by info hash, and nothing left alive knows what they were or that anyone asked for them. That is the worst kind of data loss — silent, invisible, and only noticed when the disk fills.

What is stored is what is needed to RE-ADD the transfer without contacting anybody. The magnet URI or the .torrent bytes are kept verbatim, so a restart replays from local state: the indexer may be down, rate-limiting, or gone, and none of that should cost an operator their queue.

What is deliberately NOT stored is the download URL. On a great many trackers it carries the indexer's API key in its query string, and a row that is read by every queue listing is the wrong place to keep a credential. The bytes it would have fetched are already here, so the URL has no remaining use.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `info_hash` | text | yes | — | PK |
| `title` | text | **no** | — | — |
| `indexer_id` | integer | yes | — | — |
| `indexer_name` | text | **no** | `''` | — |
| `magnet` | text | yes | — | — |
| `torrent` | blob | yes | — | — |
| `added_by` | integer | yes | — | → `app_user.id (set null)` |
| `added_label` | text | **no** | `''` | — |
| `status` | text | **no** | `'queued'` | — |
| `added_at` | text | **no** | — | — |
| `updated_at` | text | **no** | — | — |
| `completed_at` | text | yes | — | — |
| `seed_ratio` | real | **no** | `0` | — |
| `seed_secs` | integer | **no** | `0` | — |
| `uploaded_bytes` | integer | **no** | `0` | — |
| `seeded_secs` | integer | **no** | `0` | — |
| `uploaded_mark` | integer | **no** | `0` | — |
| `seeding_done_reason` | text | **no** | `''` | — |
| `target_item_id` | integer | yes | — | — |
| `target_season` | integer | yes | — | — |
| `target_episode` | integer | yes | — | — |
| `progress_bytes` | integer | **no** | `0` | — |
| `progressed_at` | text | yes | — | — |
| `stalled_at` | text | yes | — | — |
| `target_album_id` | integer | yes | — | — |
| `target_kind` | text | yes | — | — |
| `target_last_season` | integer | yes | — | — |

Indexes: `idx_download_queue_status`

## `episode`

*Migration 0013.*

One episode, as the provider knows it — whether or not this instance has it.

season_number is carried here as well as on the season row. It is denormalised deliberately: every query that matters joins episodes to media_file on (item_id, season, episode range), and forcing that through the season table adds a join to the one query — "what is missing across the whole library" — that this table exists to serve.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `item_id` | integer | **no** | — | → `media_item.id (cascade)` |
| `season_id` | integer | **no** | — | → `season.id (cascade)` |
| `season_number` | integer | **no** | — | — |
| `number` | integer | **no** | — | — |
| `title` | text | **no** | `''` | — |
| `overview` | text | **no** | `''` | — |
| `aired_at` | text | yes | — | — |
| `runtime_minutes` | integer | **no** | `0` | — |
| `provider_episode_id` | integer | yes | — | — |
| `monitored` | integer | **no** | `1` | — |
| `updated_at` | text | **no** | — | — |

Indexes: `idx_episode_aired`, `idx_episode_item`

## `feed_token`

*Migration 0024.*

Feed tokens (ADR-0041).

A calendar app and a feed reader are given an address and nothing else, so the token in the address is the credential. One per account: minting again replaces it. Only its SHA-256 is kept, so a copy of the database does not yield a working address.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `user_id` | integer | yes | — | PK, → `app_user.id (cascade)` |
| `token_hash` | text | **no** | — | — |
| `created_at` | text | **no** | — | — |
| `last_used_at` | text | yes | — | — |

## `import_record`

*Migration 0008.*

What happened to a completed download, whether or not it worked.

A failed import is the thing an operator most needs to see and the thing this category of software is worst at showing. "It downloaded and then nothing happened" is the complaint; this table is the answer.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `info_hash` | text | **no** | — | — |
| `outcome` | text | **no** | — | — |
| `detail` | text | **no** | `''` | — |
| `source_path` | text | **no** | `''` | — |
| `media_file_id` | integer | yes | — | → `media_file.id (set null)` |
| `occurred_at` | text | **no** | — | — |

Indexes: `idx_import_record_hash`

## `indexer`

*Migration 0004.*

Indexers.

The API key is stored SEALED, never as plaintext, under the context "indexer:<id>:apikey". The context is bound into the AEAD as additional data, so a sealed key cannot be relocated: lifting the blob out of one indexer's row and into another's produces a decryption failure rather than a working credential for the wrong tracker.

The id is part of the context, which is why api_key_enc is filled in by a second statement after the INSERT: the row has no id until it exists.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `name` | text | **no** | — | — |
| `kind` | text | **no** | — | — |
| `base_url` | text | **no** | — | — |
| `api_key_enc` | blob | yes | — | — |
| `categories` | text | **no** | `''` | — |
| `enabled` | integer | **no** | `1` | — |
| `priority` | integer | **no** | `25` | — |
| `seed_ratio` | real | **no** | `0` | — |
| `seed_time_secs` | integer | **no** | `0` | — |
| `created_at` | text | **no** | — | — |
| `updated_at` | text | **no** | — | — |
| `last_checked_at` | text | yes | — | — |
| `last_error` | text | yes | — | — |
| `consecutive_failures` | integer | **no** | `0` | — |
| `definition` | text | yes | — | — |
| `settings_enc` | blob | yes | — | — |

Indexes: `idx_indexer_enabled`

## `invite`

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `code_hash` | text | **no** | — | — |
| `role_id` | integer | **no** | — | → `role.id` |
| `library_ids_json` | text | **no** | `'[]'` | — |
| `rating_ceiling` | integer | **no** | `0` | — |
| `auto_approve` | integer | **no** | `1` | — |
| `issued_by_user_id` | integer | **no** | — | → `app_user.id` |
| `note` | text | yes | — | — |
| `created_at` | text | **no** | — | — |
| `expires_at` | text | **no** | — | — |
| `redeemed_at` | text | yes | — | — |
| `redeemed_by_user_id` | integer | yes | — | → `app_user.id` |
| `revoked_at` | text | yes | — | — |
| `issuer_rank` | integer | **no** | `0` | — |
| `all_libraries` | integer | **no** | `1` | — |

## `media_file`

*Migration 0008.*

One file on disk that this instance considers part of the library.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `item_id` | integer | **no** | — | → `media_item.id (cascade)` |
| `season` | integer | yes | — | — |
| `episode` | integer | yes | — | — |
| `episode_last` | integer | yes | — | — |
| `root_folder_id` | integer | **no** | — | → `root_folder.id` |
| `relative_path` | text | **no** | — | — |
| `size_bytes` | integer | **no** | `0` | — |
| `quality` | text | **no** | `''` | — |
| `revision` | integer | **no** | `0` | — |
| `release_title` | text | **no** | `''` | — |
| `release_group` | text | **no** | `''` | — |
| `info_hash` | text | yes | — | — |
| `hardlinked` | integer | **no** | `0` | — |
| `imported_at` | text | **no** | — | — |

Indexes: `idx_media_file_hash`, `idx_media_file_item`

## `media_identification`

*Migration 0010.*

What this instance believes a library item actually is, and how it came to believe it.

# Why this is a table and not three columns on media_item

media_item already carries tmdb_id, tvdb_id and imdb_id — the ANSWER. What it cannot carry is the reasoning: which candidates were considered, why one was chosen, whether a person chose it, and what the title was before. All of that is what makes an identification reviewable and reversible, and none of it belongs on a row that every library listing reads.

# Why candidates are stored rather than re-searched

Identification runs as a background pass over a whole library; review happens later, by a person, item by item. Re-searching at review time would mean a provider request per item glanced at — and worse, the thing confirmed might not be the thing proposed, because the provider's results moved in between. A person must confirm what they were shown.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `item_id` | integer | yes | — | PK, → `media_item.id (cascade)` |
| `state` | text | **no** | — | — |
| `provider` | text | **no** | `''` | — |
| `provider_id` | integer | yes | — | — |
| `parsed_title` | text | **no** | — | — |
| `parsed_year` | integer | yes | — | — |
| `decided_by` | integer | yes | — | → `app_user.id (set null)` |
| `decided_at` | text | yes | — | — |
| `verdict` | text | **no** | `''` | — |
| `verdict_why` | text | **no** | `''` | — |
| `searched_at` | text | yes | — | — |
| `updated_at` | text | **no** | — | — |

Indexes: `idx_media_identification_state`

## `media_identification_candidate`

*Migration 0010.*

The candidates a person chooses between.

Deliberately a copy rather than a reference to anything: this is what was SHOWN, and it must not change underneath the person deciding. A poster path that moved or a title the provider edited would otherwise silently rewrite the question.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `item_id` | integer | **no** | — | PK, → `media_item.id (cascade)` |
| `provider` | text | **no** | — | PK |
| `provider_id` | integer | **no** | — | PK |
| `rank` | integer | **no** | — | — |
| `title` | text | **no** | — | — |
| `original_title` | text | **no** | `''` | — |
| `year` | integer | yes | — | — |
| `overview` | text | **no** | `''` | — |
| `poster_path` | text | **no** | `''` | — |
| `score` | real | **no** | `0` | — |
| `why` | text | **no** | `''` | — |

Indexes: `idx_media_ident_candidate_rank`

## `media_issue`

*Migration 0025.*

A problem with a title, reported by somebody watching it (ADR-0042).

One title, one kind of problem, optionally one episode. At most one OPEN issue per problem: a second report of the same is the first, so a household noticing one broken subtitle is one line, not five.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `item_id` | integer | **no** | — | → `media_item.id (cascade)` |
| `kind` | text | **no** | — | — |
| `season` | integer | **no** | `0` | — |
| `episode` | integer | **no** | `0` | — |
| `note` | text | **no** | `''` | — |
| `reported_by` | integer | yes | — | → `app_user.id (set null)` |
| `reported_at` | text | **no** | — | — |
| `state` | text | **no** | `'open'` | — |
| `resolved_by` | integer | yes | — | → `app_user.id (set null)` |
| `resolved_at` | text | yes | — | — |
| `resolution` | text | **no** | `''` | — |

Indexes: `idx_media_issue_open`, `idx_media_issue_state`

## `media_item`

*Migration 0008.*

The library itself: what this instance holds, and where each file is.

# What is deliberately NOT here

There is no episode table listing episodes the instance does NOT have. That table is what drives "wanted", a calendar, and season-completion percentages, and it cannot be populated without a metadata provider telling us which episodes exist. Inventing rows from the files we happen to hold would produce a season that is always 100% complete, which is worse than having no answer. It arrives with the metadata increment.

There is no season table either, for now. A season is `SELECT DISTINCT season FROM media_file WHERE item_id = ?`. It earns a table when it has attributes of its own — a poster, an overview, a monitored flag — and all three come from metadata.

A thing a person browses to: one film, or one series.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `kind` | text | **no** | — | — |
| `title` | text | **no** | — | — |
| `year` | integer | yes | — | — |
| `sort_title` | text | **no** | — | — |
| `root_folder_id` | integer | **no** | — | → `root_folder.id` |
| `folder` | text | **no** | — | — |
| `tmdb_id` | integer | yes | — | — |
| `tvdb_id` | integer | yes | — | — |
| `imdb_id` | text | yes | — | — |
| `added_at` | text | **no** | — | — |
| `updated_at` | text | **no** | — | — |
| `episodes_refreshed_at` | text | yes | — | — |
| `monitored` | integer | **no** | `1` | — |
| `quality_profile_id` | integer | yes | — | → `quality_profile.id (set null)` |
| `certification` | text | yes | — | — |
| `rating_rank` | integer | yes | — | — |
| `rating_source` | text | yes | — | — |
| `rating_checked_at` | text | yes | — | — |
| `musicbrainz_id` | text | yes | — | — |
| `openlibrary_id` | text | yes | — | — |
| `author` | text | yes | — | — |
| `follow_new_seasons` | integer | **no** | `1` | — |
| `season_folders` | integer | **no** | `1` | — |
| `daily` | integer | **no** | `0` | — |

Indexes: `idx_media_item_musicbrainz`, `idx_media_item_root`, `idx_media_item_sort`

## `media_probe`

*Migration 0011.*

Migration 0011: what is actually inside a media file.

Everything before this knows a file by its NAME — the release title a stranger chose, parsed for a quality and a resolution. That is a claim. This is the file itself, read by ffprobe in the sandbox described in ADR-0020, and it is what every playback decision is made from.

A probe is cached per file, and invalidated by the file changing.

The temptation is to probe once at import and never again. That is wrong, because a file CAN change underneath the library: a re-download, an operator's own re-encode, a restore from a backup that was not the same file. A stale probe then produces a playback failure that looks like a bug in the player and is impossible to diagnose from the outside.

So size_bytes and mod_time record the file's identity AT THE MOMENT IT WAS PROBED. Anything that does not match means re-probe. It is not a hash — a hash would be exact and would mean reading every byte of a 40 GB remux to find out whether anything changed, which costs far more than the case it catches.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `media_file_id` | integer | yes | — | PK, → `media_file.id (cascade)` |
| `size_bytes` | integer | **no** | — | — |
| `mod_time` | text | **no** | — | — |
| `probed_at` | text | **no** | — | — |
| `sandboxed` | integer | **no** | `0` | — |
| `container` | text | **no** | `''` | — |
| `duration_ms` | integer | **no** | `0` | — |
| `bitrate` | integer | **no** | `0` | — |

## `media_request`

*Migration 0009.*

What people have asked this instance to acquire.

This is the Jellyseerr half of the brief: somebody who is not the operator says "I would like The Matrix", and somebody who IS trusted decides.

# What a request can and cannot be, today

There is no metadata provider (ADR-0016), so a request cannot be a TMDB id with a poster and a canonical title. It is WORDS: a kind, a title, a year and a note. That has a real consequence worth stating rather than hiding — two people asking for the same film in different words produce two rows unless the normalisation below happens to agree, and a request cannot be matched to a library item by id, only by title and year.

The alternative was to wait for metadata before building this at all. That is worse: a suggestion box that works is more useful than a catalogue that does not exist, and the external-id columns on media_item are already there for the day the ids arrive.

# What approval does NOT do

Approving a request does not download anything. It marks the request as something the operator is willing to acquire; a human then searches and grabs for it, and that grab is linked back here. Automatic fulfilment — the machine choosing which release satisfies which request — is a separate decision and is not built. §13 puts the legality of what this instance acquires on the operator, and a design where other people's words cause downloads without a human choosing the release is the wrong default for that.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `kind` | text | **no** | — | — |
| `title` | text | **no** | — | — |
| `year` | integer | yes | — | — |
| `note` | text | **no** | `''` | — |
| `match_key` | text | **no** | — | — |
| `state` | text | **no** | — | — |
| `requested_by` | integer | yes | — | → `app_user.id (set null)` |
| `requested_at` | text | **no** | — | — |
| `decided_by` | integer | yes | — | → `app_user.id (set null)` |
| `decided_at` | text | yes | — | — |
| `decision_reason` | text | **no** | `''` | — |
| `info_hash` | text | yes | — | — |
| `grabbed_at` | text | yes | — | — |
| `media_item_id` | integer | yes | — | → `media_item.id (set null)` |
| `fulfilled_at` | text | yes | — | — |
| `updated_at` | text | **no** | — | — |

Indexes: `idx_media_request_hash`, `idx_media_request_item`, `idx_media_request_open`, `idx_media_request_state`, `idx_media_request_user`

## `media_request_follower`

*Migration 0009.*

Everyone who wants a given request, including the person who opened it.

Separate from requested_by because "who asked first" and "who is waiting" are different questions: the first orders the queue and attributes the decision, the second decides who should be told when it arrives. Collapsing them would mean a second requester either overwrote the first or was lost.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `request_id` | integer | **no** | — | PK, → `media_request.id (cascade)` |
| `user_id` | integer | **no** | — | PK, → `app_user.id (cascade)` |
| `followed_at` | text | **no** | — | — |

## `media_stream`

*Migration 0011.*

Streams, normalised rather than stored as a JSON blob on the probe.

Because of the question ADR-0005 creates and an operator will certainly ask: "what in my library will not play on this box?" That is

SELECT ... WHERE codec = 'hevc' AND bit_depth > 8

against this table, and it is a JSON scan of every row in the other design. The library is the thing being queried, so it is modelled.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `media_file_id` | integer | **no** | — | → `media_file.id (cascade)` |
| `kind` | text | **no** | — | — |
| `stream_index` | integer | **no** | — | — |
| `codec` | text | **no** | `''` | — |
| `profile` | text | **no** | `''` | — |
| `level` | integer | **no** | `0` | — |
| `width` | integer | yes | — | — |
| `height` | integer | yes | — | — |
| `bit_depth` | integer | yes | — | — |
| `pixel_format` | text | yes | — | — |
| `frame_rate` | real | yes | — | — |
| `color_transfer` | text | yes | — | — |
| `color_primaries` | text | yes | — | — |
| `hdr` | integer | **no** | `0` | — |
| `channels` | integer | yes | — | — |
| `channel_layout` | text | yes | — | — |
| `sample_rate` | integer | yes | — | — |
| `language` | text | **no** | `''` | — |
| `title` | text | **no** | `''` | — |
| `is_forced` | integer | **no** | `0` | — |
| `is_text` | integer | **no** | `0` | — |
| `is_default` | integer | **no** | `0` | — |

Indexes: `idx_media_stream_file`, `idx_media_stream_playability`

## `password_reset`

*Migration 0003.*

0003_password_reset.sql — single-use, expiring password reset tokens.

Only the SHA-256 of the token is stored. The tokens are 256-bit random values, so a fast hash is correct here: there is nothing to brute force, and argon2 would only make verification slow on an anonymous endpoint.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `user_id` | integer | **no** | — | → `app_user.id (cascade)` |
| `token_hash` | text | **no** | — | — |
| `created_at` | text | **no** | — | — |
| `expires_at` | text | **no** | — | — |
| `used_at` | text | yes | — | — |
| `source_ip` | text | yes | — | — |
| `issued_by_user_id` | integer | yes | — | → `app_user.id` |

Indexes: `idx_password_reset_expires`, `idx_password_reset_user`

## `playback_position`

*Migration 0012.*

Migration 0012: where each person got to.

A media server without this is a toy: every session starts at zero, and a film watched over three evenings is three first acts.

One row per person per file.

PER PERSON, and that is the whole shape of the table. Two people watching the same film are in different places, and a position stored against the file alone would have each of them dragging the other back. The primary key says so rather than an application rule saying so.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `user_id` | integer | **no** | — | PK, → `app_user.id (cascade)` |
| `media_file_id` | integer | **no** | — | PK, → `media_file.id (cascade)` |
| `position_ms` | integer | **no** | — | — |
| `duration_ms` | integer | **no** | — | — |
| `finished` | integer | **no** | `0` | — |
| `updated_at` | text | **no** | — | — |

Indexes: `idx_playback_position_age`, `idx_playback_position_resume`

## `quality_profile`

*Migration 0004.*

Quality profiles.

The allowed list and the cutoff are stored as text rather than as foreign keys to a quality table: the ladder is defined in code (internal/release), and a database that disagreed with the code would be the worse authority. Profile.Compile() validates the names on the way in, so a row can only hold names the code recognises.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `name` | text | **no** | — | — |
| `allowed` | text | **no** | — | — |
| `cutoff` | text | **no** | — | — |
| `preferred` | text | **no** | `'[]'` | — |
| `required` | text | **no** | `'[]'` | — |
| `forbidden` | text | **no** | `'[]'` | — |
| `builtin` | integer | **no** | `0` | — |
| `created_at` | text | **no** | — | — |
| `updated_at` | text | **no** | — | — |
| `is_default` | integer | **no** | `0` | — |

Indexes: `idx_quality_profile_one_default`

## `recovery_code`

*Migration 0001.*

Recovery codes are hashed exactly like passwords. With mandatory MFA they are the only path back into a locked-out account, so they are single-use and their consumption is audited.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `user_id` | integer | **no** | — | → `app_user.id (cascade)` |
| `code_hash` | text | **no** | — | — |
| `created_at` | text | **no** | — | — |
| `used_at` | text | yes | — | — |

Indexes: `idx_recovery_code_user`

## `role`

*Migration 0001.*

0001_identity.sql — Phase 1 schema: roles, users, account requests, invites, sessions, API tokens, library grants, audit log, auth throttling, settings.

Conventions: * Timestamps are RFC3339 UTC strings. SQLite has no date type and text sorts correctly in this format. * "app_user" rather than "user" to avoid any reserved-word ambiguity. * Every foreign key is declared; PRAGMA foreign_keys is ON per connection.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `name` | text | **no** | — | — |
| `rank` | integer | **no** | — | — |
| `builtin` | integer | **no** | `0` | — |
| `created_at` | text | **no** | — | — |
| `updated_at` | text | **no** | — | — |
| `permissions_chosen_at` | text | yes | — | — |

## `role_permission`

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `role_id` | integer | **no** | — | PK, → `role.id (cascade)` |
| `permission` | text | **no** | — | PK |

## `root_folder`

*Migration 0007.*

Library root folders.

A root folder is where the library physically lives, and it is the boundary every destructive operation is checked against. `internal/library` opens each one as an os.Root — a kernel-enforced containment handle — so a path derived from anything a stranger chose (a torrent's declared name, a release title, a metadata provider's answer) cannot reach outside it. Not by "..", not by an absolute path, and not by a symlink planted inside the tree, which is the vector filepath.Clean does not catch and EvalSymlinks races on.

Roots MUST NOT nest. If root A contains root B then "which root owns this file" has two answers, and a delete authorised against A reaches into B. The store refuses a nested root at creation rather than leaving that ambiguity to be discovered by a deletion.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `path` | text | **no** | — | — |
| `kind` | text | **no** | — | — |
| `label` | text | **no** | `''` | — |
| `hardlinks_ok` | integer | **no** | `0` | — |
| `hardlink_note` | text | **no** | `''` | — |
| `free_bytes` | integer | **no** | `0` | — |
| `checked_at` | text | yes | — | — |
| `created_at` | text | **no** | — | — |
| `updated_at` | text | **no** | — | — |

Indexes: `idx_root_folder_kind`

## `root_folder_grant`

*Migration 0022.*

The root folders a restricted account may see. Forgetting a root forgets its grants: an account restricted to roots that are all gone sees nothing, which is the failure that is safe.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `user_id` | integer | **no** | — | PK, → `app_user.id (cascade)` |
| `root_folder_id` | integer | **no** | — | PK, → `root_folder.id (cascade)` |
| `granted_at` | text | **no** | — | — |

## `schema_migration`

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `version` | integer | yes | — | PK |
| `name` | text | **no** | — | — |
| `checksum` | text | **no** | — | — |
| `applied_at` | text | **no** | — | — |

## `season`

*Migration 0013.*

Seasons and episodes: what a series HAS, and what it is missing.

# Why these tables exist now and did not before

0008_media.sql refused to create them, and named the condition:

"it cannot be populated without a metadata provider telling us which episodes exist. Inventing rows from the files we happen to hold would produce a season that is always 100% complete, which is worse than having no answer."

Phase 3 wired a provider. This is that increment (ADR-0022).

The rule these tables are built around: a row here exists because a PROVIDER says the episode exists. Nothing creates one from a filename, a release name, or a file on disk. A season assembled from what is on disk is complete by construction, and a "missing" list built the same way is always empty.

One season of a series.

0008 said a season "earns a table when it has attributes of its own — a poster, an overview, a monitored flag — and all three come from metadata". The deciding one is monitored: an episode that has not aired yet has no row to inherit from, so the operator's intent has to live somewhere that exists before the episode does.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `item_id` | integer | **no** | — | → `media_item.id (cascade)` |
| `number` | integer | **no** | — | — |
| `name` | text | **no** | `''` | — |
| `overview` | text | **no** | `''` | — |
| `episode_count` | integer | **no** | `0` | — |
| `aired_at` | text | yes | — | — |
| `monitored` | integer | **no** | `1` | — |
| `updated_at` | text | **no** | — | — |

## `session`

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | text | yes | — | PK |
| `user_id` | integer | **no** | — | → `app_user.id (cascade)` |
| `refresh_hash` | text | **no** | — | — |
| `refresh_generation` | integer | **no** | `0` | — |
| `mfa_satisfied` | integer | **no** | `0` | — |
| `device_label` | text | yes | — | — |
| `source_ip` | text | yes | — | — |
| `user_agent` | text | yes | — | — |
| `created_at` | text | **no** | — | — |
| `last_seen_at` | text | **no** | — | — |
| `idle_expires_at` | text | **no** | — | — |
| `absolute_expires_at` | text | **no** | — | — |
| `revoked_at` | text | yes | — | — |
| `revoked_reason` | text | yes | — | — |
| `prev_refresh_hash` | text | yes | — | — |
| `rotated_at` | text | yes | — | — |

Indexes: `idx_session_user`

## `setting`

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `key` | text | yes | — | PK |
| `value` | text | **no** | — | — |
| `updated_at` | text | **no** | — | — |

## `subtitle_search`

*Migration 0030.*

What the subtitle sweep last did about each file and language (ADR-0056): when it searched, what came of it, and when it may search again. Forgotten with the file.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `media_file_id` | integer | **no** | — | PK, → `media_file.id (cascade)` |
| `language` | text | **no** | — | PK |
| `searched_at` | text | **no** | — | — |
| `next_at` | text | **no** | — | — |
| `fruitless` | integer | **no** | `0` | — |
| `outcome` | text | **no** | — | — |
| `detail` | text | **no** | `''` | — |

## `track`

*Migration 0026.*

A track of an album's chosen release. file_id is the library file that holds it, once one does.

| Column | Type | Null | Default | Key |
|---|---|---|---|---|
| `id` | integer | yes | — | PK |
| `album_id` | integer | **no** | — | → `album.id (cascade)` |
| `disc` | integer | **no** | — | — |
| `number` | integer | **no** | — | — |
| `title` | text | **no** | — | — |
| `length_ms` | integer | yes | — | — |
| `musicbrainz_id` | text | yes | — | — |
| `file_id` | integer | yes | — | → `media_file.id (set null)` |

Indexes: `idx_track_file`

