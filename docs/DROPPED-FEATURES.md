# Deliberately not built

Every entry here is a thing the software being replaced does and this one does
not. They are listed together because a feature gap that is written down is a
decision, and a feature gap that is discovered is a bug report.

Two kinds appear below: **refused** — will not be built, with a reason — and
**not yet** — in scope, not done, and not pretended otherwise.

---

## Refused

### Unpacking archives — `.rar`, `.zip`, `.7z`, `.001`

Every competitor does this, because usenet and some trackers still ship that
way. It means running a decompressor over attacker-controlled input and writing
whatever comes out into a library path — a process-boundary problem
([ADR-0007](adr/0007-process-boundaries.md)), not a convenience.

`internal/importer/select.go` skips them with a stated reason rather than
half-supporting them. **This is a real gap**: usenet grabs that arrive as a
multi-part RAR will not import. Revisitable only behind a sandboxed executor.

### Disc images — `.iso`, `.img`

Cannot be direct-played, cannot be probed for streams without mounting, and
importing one produces a library entry nothing can play. Skipped with a reason
beats imported and broken.

### Custom post-processing scripts

Refused outright, and the only entry here with no path back.

A post-processing hook is arbitrary operator-supplied code, executed by a
service running with filesystem access to the library, triggered by a file
downloaded from a stranger. It is the single largest remote-code-execution
surface in the *arr ecosystem, it exists to work around missing features, and
adding it would undo the containment the rest of this design is built on.

### A Jellyfin/Emby/Plex client API shim

No Jellyfin clients will work — no Android TV app, no Infuse, no Roku channel.
This is the largest deliberate cost in the project.
[ADR-0006](adr/0006-no-jellyfin-shim.md) gives the three reasons; the short one
is that Jellyfin's client protocol has nowhere to put mandatory MFA, so a shim
would carve a hole in the one rule §2 states most plainly.

### Adaptive bitrate ladders

Encoding the same content several times at once, on four Skylake cores, at five
concurrent viewers, converts a working instance into a broken one. One output
per session ([ADR-0005](adr/0005-transcode-policy-skylake.md)).

### HDR tone mapping

The target hardware cannot decode HEVC Main10 and cannot tone-map in software at
realtime. Attempting it produces the worst outcome available — a stuttering
stream that also degrades the sessions that were working. 4K HDR direct-plays or
it does not play, and the player says which.

Not mitigated by a setting. The honest fix is newer hardware.

### A Postgres portability layer

[ADR-0004](adr/0004-sqlite-only-v1.md). An abstraction written for an engine
nobody runs supports only the subset somebody imagined, and makes every query
worse forever in exchange for a migration that may never happen.

### React, Vite, and a JavaScript build step

The Phase 0 brief asked for React + TypeScript + Vite + TanStack Query +
Tailwind. Overruled in [ADR-0011](adr/0011-server-rendered-shells-no-build-step.md)
after the first UI increment: for an application that is mostly forms and lists
over a REST API, the build step was all cost — a dependency tree larger than the
entire rest of the project, an audit surface, and a lockfile to maintain — for
convenience that hand-written JavaScript did not need.

The assets served are the assets in the repository. There is no `node_modules`.

### Telemetry, phone-home, and mandatory accounts

Never. The only outbound calls are to indexers and to the configured metadata
provider, both operator-supplied.

### An internal event bus

Asked for in Phase 0 alongside typed interfaces. The interfaces exist; the bus
does not, because nothing has yet wanted to notify a listener it does not
already hold a reference to. Adding one before a second subscriber exists would
be adding indirection to a direct call.

---

## Not yet

### Music and books — the Lidarr and Readarr replacements

**Begun in 5a** ([ADR-0044](adr/0044-music-artists-albums-and-tracks.md)): artists
are followed from MusicBrainz with their albums and tracks, and what is missing
is on the Wanted list. 5b scans and imports their files
([ADR-0045](adr/0045-music-files.md)); 5c searches for an album, grabs it and
imports what arrives ([ADR-0046](adr/0046-searching-for-and-grabbing-an-album.md)).
5d fetches wanted albums automatically, once each
([ADR-0047](adr/0047-fetching-wanted-albums-automatically.md)); 6l upgrades a
held album from lossy to lossless when upgrades are on
([ADR-0062](adr/0062-upgrading-albums-to-lossless.md)). **Not yet:** a second
automatic grab for an album still missing tracks, watching the recent-release feed for albums, reading bit
rate or tags from the files. 5e adds books from Open Library one at a time
([ADR-0048](adr/0048-books-from-open-library.md)), and 5f gives a book its
file: scanned, searched for, grabbed and imported
([ADR-0049](adr/0049-a-books-files.md)). 5g fetches wanted books automatically
([ADR-0050](adr/0050-fetching-wanted-books-automatically.md)). **Not yet:**
audiobooks (deferred by the owner on 2026-10-03, to be added if it is needed); watching the recent-release feed for books. **Not planned:** following an author (Open
Library's works of an author are not a bibliography), or reading a book in
the browser (it is served as its file).

Music in particular is not "films with different metadata" — it is a different
identification problem (releases, editions, per-track tagging), a different
provider (MusicBrainz, not TMDB), and a different quality model. It is closer to
a second application than a fourth kind.

Recorded here so that "80–90% parity" is read correctly: the parity
approached first was with Radarr, Sonarr, Prowlarr, qBittorrent, Jellyseerr and
Jellyfin. Lidarr and Readarr were begun in Phase 5 and cover their core loop —
follow, find, grab, import — without the gaps listed above.

### Subtitle *fetching* — the Bazarr replacement

Half of this is built and half is not, and the line between them is worth being
precise about.

**Built (increment 4f).** Subtitles a library already holds are served:
embedded text tracks and sidecar files, converted to WebVTT on the way out,
with bitmap tracks listed and refused with a reason. `SubtitlePath` puts a
sidecar beside its video on import.

**Built (increment 6e,** [ADR-0055](adr/0055-fetching-a-subtitle.md)**).** A
file's subtitle is fetched from OpenSubtitles.com on request, with the
operator's own API key: matched by the title and the file's hash, ranked, and
written beside the video as an SRT. The languages wanted are a setting.

**Built (increment 6f,** [ADR-0056](adr/0056-fetching-wanted-subtitles-automatically.md)**).**
A scheduled sweep fetches wanted subtitles without a person: every film's and
episode's file lacking one in a wanted language, ten searches a pass, backing
off from a day to a month when nothing is found.

**Not built.** Other providers than OpenSubtitles; and anything for a subtitle
that is a picture (PGS, VobSub), which would need OCR.

### Adding what you want to follow — series and films

**Built (increment 4n).** A series can be added from the metadata provider
before any of it is on disk ([ADR-0025](adr/0025-adding-a-series-before-it-is-on-disk.md)):
choose it from the provider's search, choose which of its episodes you want —
*all*, *future*, *latest* or *none*, with no default — and it exists with every
season and episode the provider lists. Nothing is downloaded and nothing is
created on disk; its folder appears with the first episode imported into it.

**Built (increment 4o).** A film is added the same way, with no choice to make:
adding a film is wanting it, and it goes on the Wanted list
([ADR-0026](adr/0026-adding-a-film-and-searching-for-it.md)). It is searched for
from its page or from Wanted, and every release the indexers offer is judged
against it — its title, original title and the provider's alternative titles,
and its year, within one either way; a release with no year is refused, because
remakes share titles. Only a release that **is** the film can be grabbed, and the
film is sealed into the grab, so the import files it under that film whatever
the release calls itself: `Star.Wars.Episode.IV.A.New.Hope.1977` becomes
*Star Wars (1977)*, not a second film beside it.

**Not built.**

- ~~**Monitoring a film.**~~ **Built in increment 4v**
  ([ADR-0030](adr/0030-automatic-acquisition.md)), with the automatic search it
  needed: a film can be kept in the library and off the Wanted list.
- **Release dates.** A film is wanted from the moment it is added, out or not:
  the provider's date is not stored or refreshed, and a rule on a stale date
  hides films. A film still in cinemas turns up camera recordings; every
  built-in profile except *Any* forbids them, and since increment 4p every
  search is judged by the instance's default profile unless the person picks
  another ([ADR-0027](adr/0027-a-default-quality-profile.md)), so a camera
  recording is grabbable only when somebody chooses *Any* or *No profile*.
- **Searching by IMDb id.** The film search asks by title and year. Which
  indexers support an id search is in capabilities this software does not read.
- **A quality profile per series or per film.** Sonarr and Radarr keep one on
  each. Here one instance-wide default judges every search that names none —
  *HD-1080p* on a new instance, changed by an administrator — and the person
  searching can pick another or none. A profile per item is what automatic
  searching would need, and it can be added on top of the default. Nor is there
  a screen that edits a profile: the four built-in ones can be chosen between,
  not changed.
- **Changing a series' choice after it is added.** The four choices exist; only
  the add applies one. Afterwards, seasons and episodes are switched one at a
  time, as before.
- ~~**Adding from an approved request.**~~ **Built in increment 4q**
  ([ADR-0028](adr/0028-an-approved-request-is-satisfied-by-a-library-item.md)):
  an approved request is added to the library from the Requests screen and
  linked to what was added, and it is fulfilled when a file of that title
  arrives. Approving still creates nothing.
- **Posters in the Add screen's search results.** The poster route fetches only
  for titles this instance has recorded (ADR-0018's bound), and a search result
  is not recorded until it is added.

### Episodes — tracked, searchable, and grabbed automatically when that is on

**Built (increment 4k).** A series knows which episodes exist, because the
metadata provider says so and nothing else may
([ADR-0022](adr/0022-episode-tracking.md)): seasons and episodes are refreshed
from TMDB, files are matched to them by number — a file holding `S01E01E02`
counts for both — and the **wanted** list is monitored, aired and absent.
Seasons and episodes can be switched on and off; specials start off; an episode
the provider lists with no date is *announced*, not overdue. A scheduled task
keeps running series current and asks about every other series weekly, so a
renewal is noticed.

**Built (increment 4l).** A wanted episode can be searched for from the series
page or the Wanted screen ([ADR-0023](adr/0023-searching-for-a-wanted-episode.md)).
Every result is judged against the episode — series name including the
provider's alternative titles, year, season, episode or range — and only one
that IS the episode can be grabbed. The grab carries the episode, sealed, to
the download queue, and the import files it under that series whatever the
release calls it.

**Not built.**

- ~~**Grabbing automatically.**~~ **Built in increment 4v**
  ([ADR-0030](adr/0030-automatic-acquisition.md)), off unless the configuration
  turns it on. Of what this list said it would need: a per-run cap and pauses —
  yes, a budget of searches and grabs per pass and a back-off; a principal of its
  own — yes, `system:acquire`; no alternative titles that are only translations
  — done on the release instead, which is refused when it is named in a
  language; alternative titles cached for a day rather than stored. A per-series
  opt-in beside monitoring and a match with no year tolerance were weighed and
  not done, for the reasons that record gives: monitoring *is* the per-series,
  per-season and per-episode opt-in, and without the tolerance the right release
  is refused.
- **Searching the whole wanted list at once.** Same reasons. ~~A whole season~~
  is searched as one since increment 4y.
- ~~**Season packs.**~~ **Built in increment 4y**
  ([ADR-0033](adr/0033-season-packs.md)): a person grabs one from a season's
  search, automatic acquisition grabs one for a settled season it wants all of,
  and the import files each file as the episode its own name says. Packs of
  several seasons and complete series are grabbed by a person from a season's
  search since increment 6g ([ADR-0057](adr/0057-multi-season-packs.md)), each
  file under its own season. **Still not built:** automatic acquisition of a
  multi-season pack, and a file in a pack whose name gives no episode number
  (`03 - Title.mkv`), which is left in the download.
- ~~**A calendar.**~~ An iCal feed of a month back and three months ahead was
  built in 4ag ([ADR-0041](adr/0041-feeds.md)). There is still no calendar
  *screen*: a calendar app is better at that than a page would be.
- **Absolute numbering.** Anime released as `E087` with no season does not map
  onto a season and episode without a scene-numbering table. Such files import
  as files with no episode beside them. Deferred by the owner on 2026-10-03,
  to be added if it is needed: it would mean contacting a third-party
  numbering service such as TheXEM.
- **Per-series settings** Sonarr has — the *anime* series type, which is
  absolute numbering above. ~~Daily series~~ are matched, searched and filed by
  air date since 6n ([ADR-0064](adr/0064-daily-series.md)). ~~A
  "monitor new seasons" switch~~ is built in 6k
  ([ADR-0061](adr/0061-following-new-seasons.md)) and ~~season folders~~ in 6m
  ([ADR-0063](adr/0063-season-folders.md)); turning every regular season off
  still also says "I have stopped following this".

### Indexers on your own network — built, with its limits

**Built (increment 4m).** A Prowlarr or Jackett on the LAN, on the same host or
in the same compose file now works ([ADR-0024](adr/0024-indexers-on-the-operators-network.md)).
It did not before: the indexer egress profile refuses private addresses as SSRF
protection, and that refused the ordinary way to run an aggregator.

The exemption is exactly the host and port you type as that indexer's address.
Its own download links work, because Prowlarr and Jackett proxy downloads
through themselves. A link or redirect in its feed to **any other** private
address — another port on the same machine, the router, another indexer — is
refused as before, and an indexer at a link-local address (where cloud
metadata services live) is refused when you save it.

**Not handled.**

- **Through a SOCKS5 proxy** a LAN indexer is unreachable: the proxy is not on
  your network. Keep the `indexer` profile in `direct` mode for one.
- **A different spelling of the same machine** is not the address you typed. If
  Prowlarr's download links say `http://prowlarr:9696` and you configured
  `http://192.168.1.10:9696`, the links are refused, with the host named;
  configure the indexer by the name its links use.
- **Non-ASCII hostnames** do not match, and are refused.

### Per-library visibility and rating ceilings

**Built in 4ac** ([ADR-0037](adr/0037-libraries-and-rating-ceilings.md)): a library is a
root folder, and a rating ceiling hides what is rated above it, and whatever is
unrated. What it does not do:

- **Other countries' certifications.** Ratings are the US ones, for every
  instance; a title can be rated by hand.
- **A rating per episode.** A series has one.
- **A library that is not a root folder** — a collection spanning roots, or a
  tag. One root per library is what people already do.

### Talking to Cardigann-defined trackers directly

Torznab and Newznab are built, and trackers are searched from their Cardigann
definitions: public ones since increment 6h
([ADR-0058](adr/0058-cardigann-public-definitions.md)), and ones that sign in
— by form, post, get or a browser's cookie — since 6i
([ADR-0059](adr/0059-cardigann-sign-in.md)). The operator pastes a tracker's
YAML from Jackett's or Prowlarr's repository, and it is checked when saved.
A download link only on a details page is read from it when the release is
grabbed, since 6j ([ADR-0060](adr/0060-cardigann-download-from-the-details-page.md)).
**Not yet:** a captcha, two-factor sign-in or a Cloudflare challenge, and a
definition's `download.before` request or `infohash` form. A Prowlarr or
Jackett on your own network (above) still reaches those.

### Migration importers — Radarr built; the rest dropped

**Scope change, 2026-09-26.** The operator is starting a new library rather than
migrating one, so migration is no longer planned beyond what exists. The Radarr
importer stays: it does nothing unless a Radarr database is placed in the
migration directory, and removing working, tested code nobody asked to remove
would be a change of its own.

**Built (increment 4g).** Radarr: the identities. A Radarr database is read
read-only and the TMDB and IMDb ids it holds are attached to library items
matched by folder name ([ADR-0021](adr/0021-migrating-from-radarr.md)). That is
the expensive part of adopting this software with an existing library; the files
themselves are found by the ordinary scan.

**Not built.**

- **Sonarr — dropped** with the scope change above. What it would have needed
  is kept here in case that changes: unblocked since increment 4k, which gave
  series the episode tracking a Sonarr migration needs somewhere to land. The approach
  is Radarr's — attach an identity, then let the ordinary refresh fetch the
  episodes — with one step Radarr did not need: Sonarr identifies a series by
  its **TVDB** id, and this software uses TMDB's. Whether a given Sonarr
  database also holds a TMDB id depends on its version, which I have not
  verified; where it does not, each series needs a lookup through TMDB's
  `/find` endpoint, documented to accept a TVDB id and not yet tried here.
- **Lidarr and Readarr.** No target domain; see above.
- **Prowlarr.** Its indexer rows carry API keys, which is a credential-handling
  problem rather than a data-shape one and deserves its own decision.
- **qBittorrent `BT_backup`.** In-flight downloads, not library data. Resume
  data is bencoded and version-specific, and the cheap alternative — let the
  remaining torrents finish in qBittorrent — costs an operator nothing.
- **Radarr's quality profiles, tags, monitored state, history and download
  clients.** They describe how Radarr was configured, not what the library is,
  and this software's equivalents are not the same objects.

### Playback — what Phase 4 left out

Phase 4 is built: probing in a jail, direct play over range requests, resume,
remuxing for a browser that cannot decode the soundtrack, and the subtitles
above. What it does not do:

- **No video transcode.** If the browser cannot decode the *picture* — HEVC,
  10-bit, HDR — the refusal is final and says so
  ([ADR-0005](adr/0005-transcode-policy-skylake.md)). 4K HDR is
  direct-play-only on this hardware, by design.
- **No HLS.** The play-session routes Phase 1 registered for it were removed in
  4ad ([ADR-0038](adr/0038-the-library-routes-left-unbuilt.md)): desktop browsers
  need a third-party player for it, which ADR-0011 refuses.
- **No seeking within a converted stream.** The output is a pipe being produced
  as it is watched, so there is no file to serve a byte range from. The response
  says `Accept-Ranges: none` rather than offering a scrubber that does nothing.
  Fixing it means restarting ffmpeg at an offset and having the player carry
  that offset in its own arithmetic.
- **No client-side capability negotiation beyond `canPlayType`.** The server's
  opinion is conservative and the browser is asked before a refusal stands.

### Smaller flagged items

Carried in PROGRESS.md rather than forgotten: ~~per-file delete, on-demand trash purge~~ (built in
6c, [ADR-0053](adr/0053-deleting-one-file-and-purging-now.md)), ~~master-key
rotation~~ (built in 6d, [ADR-0054](adr/0054-rotating-the-master-key.md)), ~~breached-password checking, signup proof-of-work~~ (built in 6a,
[ADR-0051](adr/0051-the-breach-check-and-signup-proof-of-work.md)), and ~~the AGPL source
offer in the UI~~ (built in 6b, [ADR-0052](adr/0052-offering-the-source.md)). (This list used to include a tightened seccomp profile, done
since 4c.)
