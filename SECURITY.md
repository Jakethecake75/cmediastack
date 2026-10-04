# Security

CMediaStack is a single-operator, self-hosted application that is exposed to
the public internet in its reference deployment. This document states what the
security controls actually are, so that an operator can verify them rather than
trust them.

The threat model is in [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md).

---

## Reporting a vulnerability

Open a private security advisory on the repository, or contact the operator
directly. Do not open a public issue. There is no bounty; there is a
commitment to fix and to credit.

---

## What is enforced today (Phase 1)

Each item names the test that proves it. If a claim here has no test, it is a
plan, not a control, and it is listed under "Not yet enforced" instead.

| Control | Proof |
|---|---|
| Every route outside the anonymous allowlist rejects anonymous requests with 404 — except the root, which redirects to the login page ([ADR-0012](docs/adr/0012-root-redirect-carve-out.md)) | `api.TestEveryNonAllowlistedRouteRejectsAnonymous` — enumerates all 69 protected routes, and asserts the single carve-out is a bare redirect with no body |
| The anonymous allowlist and the route registrations cannot disagree | `api.TestAllowlistAndRegistrationsAgree`, and `Router.register` panics on a mismatch |
| An anonymous route cannot be added without a deliberate allowlist entry | `api.TestRegisteringUnlistedAnonymousRoutePanics` |
| An approved account cannot act before enrolling an authenticator | `api.TestAwaitingMFAReachesOnlyEnrollment` |
| A session that never presented a second factor cannot act | `api.TestSessionWithoutSecondFactorReachesNothing` |
| Suspended and disabled accounts reach nothing | `api.TestSuspendedAndDisabledReachNothing`, `authz.TestSuspendedAdminHoldsNothing` |
| A low-privilege user cannot reach any admin route, and gets 404 rather than 403 | `api.TestUserCannotReachAnyAdminRouteAndGets404` — every hidden route in the routing table, 44 |
| A Manager cannot reach Admin-only routes | `api.TestManagerCannotReachAdminOnlyRoutes` |
| A Manager cannot assign Admin or Manager, or grant a permission they lack | `authz.TestManagerCannotAssignAdminRole`, `TestManagerCannotGrantUnheldPermissions` |
| Nobody, including an Admin, can modify their own role | `authz.TestNobodyCanModifyTheirOwnRole` |
| A Manager with queue access cannot destroy media bytes | `authz.TestManagerWithQueueAccessCannotDestroyMediaBytes` |
| An effect with no permission mapping fails closed | `authz.TestUnmappedEffectFailsClosed` |
| A nil principal holds nothing and matches no rows | `authz.TestNilPrincipalHasNothing`, `TestAnonymousScopeMatchesNothing` |
| Secrets are AES-256-GCM sealed and context-bound | `secrets.TestContextBindingPreventsCiphertextRelocation`, `TestTamperingIsDetected` |
| Decryption errors do not distinguish failure modes | `secrets.TestDecryptErrorsAreIndistinguishable` |
| Credentials and tokens never reach the log | `logging.TestSensitiveAttributeKeysAreRedacted` and five further redaction tests |
| Password hashing is Argon2id, and a malformed hash is indistinguishable from a wrong password | `identity.TestMalformedHashIsIndistinguishableFromWrongPassword` |
| TOTP matches RFC 6238 | `identity.TestTOTPMatchesRFC6238Vectors` |
| The TOTP acceptance window is exactly ±1 step | `identity.TestVerifyTOTPHonoursSkewWindowAndNoMore` |
| The audit log exposes no mutation method | `audit.TestLoggerExposesNoMutationMethods` |
| Reading the audit log requires the permission, at the data layer | `audit.TestListRequiresAuditPermission` |
| A denial the client saw as 404 is recorded as a real denial | `audit.TestAuthzDeniedIsRecorded` |
| The audit log is read by an administrator only: both routes are invisible to everyone else, the data layer checks the permission again, and each refusal is itself a line | `api.TestTheAuditLogIsHiddenFromEveryoneElse`, `audit.TestReadingNeedsThePermission` — mutation-verified (ADR-0031) |
| A filter the log cannot answer is refused rather than answered with everything, and text is matched as itself, not as a pattern | `api.TestAnAuditFilterThatCannotBeIsRefused`, `audit.TestABadFilterIsRefused`, `audit.TestFiltersSelectWhatTheySay` — mutation-verified |
| Pages continue by id and neither repeat nor skip a line while the log grows | `audit.TestPagesContinueByIDWithoutRepeatingOrSkipping`, `api.TestTheAuditLogReadsNewestFirstAndInPages` — mutation-verified |
| A season pack is matched only to its own single season, grabbed automatically only for a settled season wholly wanted and only once its file list is seen to hold it, and imported file by file from names alone — never replacing a better file, never choosing between two files for one episode | `search.TestASeasonPackIsOnlyItsOwnSeason`, `acquire.TestAPackIsNotGrabbedUnlessEverythingInItIsWanted`, `acquire.TestAPackMustBeSeenToHoldTheSeason`, `importer.TestPlanningAPackReadsNamesOnly`, `importer.TestAPackNeverReplacesABetterFile`, `importer.TestTwoFilesForOneEpisodeAreBothSkipped` — mutation-verified (ADR-0033) |
| The Discord webhook link is stored sealed, only after Discord confirms it, and never comes back — not in an answer, the audit log, an error or a log line; only a Discord webhook link over HTTPS is accepted, and a redirect is followed only within Discord | `notify.TestAWebhookIsCheckedSealedAndNeverWrittenDown`, `notify.TestOnlyADiscordWebhookLinkIsAccepted`, `notify.TestAWebhookNeverPrintsItsToken`, `notify.TestDiscordsAnswersAreToldApart`, `notify.TestARedirectIsFollowedOnlyWithinDiscord`, `api.TestTheWebhookLinkNeverComesBack` — mutation-verified (ADR-0032) |
| Turning notifications off, or pointing them at another webhook, is said in the channel they leave, with who did it | `notify.TestLeavingAChannelIsSaidInIt` — mutation-verified |
| A stranger's text in a notification — a username, a release name — can neither ping the server nor pass for a link: it is sent inside a code span, with mentions off and embeds suppressed | `notify.TestAStrangersTextCannotBecomeMarkup`, `notify.TestAMessageSwitchesMentionsOffAndSuppressesEmbeds`, `notify.TestAStrangersNameCannotPingOrPassForALink` — mutation-verified |
| What goes to Discord is what the categories say — titles only when the operator turns the library or requests on — and never a person's address; configuring it needs the audit-log permission as well as the settings one, and its task can read the log and nothing else | `notify.TestWhatIsSentFollowsTheCategories`, `notify.TestOnlyAnAdministratorConfiguresIt`, `api.TestWhatIsAuditedReachesTheChannel`, `authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant` — mutation-verified |
| Anonymous denials are written at most 20 an hour from one address and 120 an hour from all; an account's at most 60, and an anonymous flood can neither cut them nor, by filling the counting, lift their ceiling; what is over is counted and written as one line | `audit.TestOneAddressAddsAtMostItsCeilingAnHour`, `TestEveryAddressTogetherHasACeiling`, `TestAnAccountIsNotHiddenByAFlood`, `TestTheCountingIsBounded`, `TestTheCeilingHoldsUnderConcurrency`, `TestAStoppingServerWritesTheHourSoFar`, `api.TestAnAnonymousFloodIsCappedAndCounted` — mutation-verified (ADR-0031) |
| Foreign keys are actually enforced | `db.TestForeignKeysAreEnforced` |
| The database, its `-wal` and its `-shm` are readable by their owner alone; one created before this was fixed is narrowed when opened | `db.TestANewDatabaseIsReadableByItsOwnerAlone`, `db.TestAnExistingDatabaseOthersCouldReadIsNarrowed` — mutation-verified, and in the built container |
| The Metadata screen never holds the provider key once submitted — accepted or refused — and nothing shows it | The Chromium run asserts the field is empty after both and the key is nowhere in the page; `api.TestTheMetadataCredentialIsNeverReturned` for the API |
| An egress policy that is not enforced is never shown as a working tunnel | The Chromium run, with enforcement off and on |
| A backup is encrypted — nothing of the database readable in it — decrypts with age itself, and is recorded with who took it and its hash | `backup.TestABackupIsEncryptedCheckedAndRecorded` — mutation-verified (ADR-0029) |
| The backup passphrase derivation cannot change without failing the build, which would otherwise orphan every existing backup | `secrets.TestTheBackupPassphraseNeverChanges` — pinned to an answer from Python's `hmac` and `openssl kdf` |
| A snapshot that fails SQLite's integrity or foreign-key check, or is behind its build, is not kept; its plaintext does not outlive the attempt | `backup.TestASnapshotThatFailsItsChecksIsNotKept`, `TestADatabaseWithABrokenReferenceIsNotBackedUpAsGood`, `TestABackupIsOfTheFullyMigratedDatabase` — mutation-verified |
| An encrypted file is kept only if its bytes on disk are what was written and they decrypt to the checked snapshot | `backup.TestConfirmCatchesAFileThatIsNotWhatWasWritten` — mutation-verified |
| A backup that cannot be opened says whether it is not one, not this key's, or damaged — and one demanding more scrypt work than a backup ever does is refused before doing it | `backup.TestABackupThatCannotBeOpenedSaysWhy` — mutation-verified |
| Pruning goes by age, never below the newest `keep_min`, and touches nothing that is not a backup — not a symlink or directory named like one | `backup.TestPruningGoesByAgeKeepsTheNewestAndTouchesNothingElse`, `TestAFailingScheduleKeepsItsLastBackups` — mutation-verified |
| Taking backups deletes none | `backup.TestTakingBackupsDeletesNothing` — mutation-verified |
| No route serves or restores a backup, and the two backup routes are invisible without `admin.system` | `api.TestNoRouteServesOrRestoresABackup`, `api.TestTheBackupRoutesAreHiddenFromEveryoneElse`, `backup.TestOnlyAnAdministratorCanTakeOrListBackups` — mutation-verified |
| A restore never writes over anything, a stale `-wal` included, and never leaves a backup this build cannot read at the target | `backup.TestARestoreNeverWritesOverAnything`, `TestABackupFromANewerBuildIsNotRestored`, `TestABackupOfADamagedDatabaseIsNotRestored` — mutation-verified |
| A restored database has no live session or API token, and records the restore | `backup.TestARestoredDatabaseIsFencedBeforeItIsHandedOver` — mutation-verified |
| A named backup directory is never created | `backup.TestANamedBackupDirectoryIsNeverCreated` — mutation-verified |
| A forwarded header is ignored unless a trusted proxy is configured | `api.TestForwardedHeaderIsIgnoredWithoutTrustedProxies` |
| A suspended account is byte-identical to an unknown one at the root, so the redirect is not an oracle | `api.TestSuspendedAccountIsIndistinguishableFromAnonymousAtTheRoot` — status, `Location` and body |
| The anonymous JavaScript bundle names no authenticated endpoint | `web.TestAnonymousBundleNamesNoAuthenticatedEndpoint` — scans it for any `/api/` path outside a nine-entry allowlist |
| No page emits inline script, inline style or an `on*` handler, so the CSP needs no `unsafe-inline` | `web.TestTemplatesContainNothingInline` |
| No shipped JavaScript uses `innerHTML`, `eval` or `document.write` | `web.TestScriptsAvoidBlockedAndUnsafeAPIs` |
| The asset handler serves only flat, known names from its own bundle | `web.TestAssetsRefuseAnythingButAFlatKnownName`, `TestAssetHandlerIsConfinedToItsBundle`, `TestTemplatesAreNotServable` |
| An approver is offered only roles they may actually assign, least-privileged first | `api.TestAssignableRolesPutTheSafestOptionFirst` |
| An unmatched path is a 404, not the application shell | `api.TestUnmatchedPathIs404ForAnonymous` |
| An unmatched path is byte-identical to a hidden route the caller may not have — same status, body and headers | `api.TestUnmatchedPathIsByteIdenticalToADeniedHiddenRoute` |
| No API response is cacheable, so a shared cache never holds a once-shown secret | `api.TestSecretBearingResponsesAreNotCacheable` |
| The enrollment QR carries the real otpauth URI | `api.TestEnrollmentQRDecodesToTheRealOTPAuthURI` — decoded with an independent library |
| A hostile release name cannot hang, panic, or smuggle a control character or invalid UTF-8 into a title | `release.TestHostileNamesAreSurvived`, `release.FuzzParse` |
| Release-name matching is linear-time (RE2), so a crafted name cannot burn CPU | `release.TestPathologicalInputIsLinear` |
| An operator's own profile pattern cannot hang the release pipeline | `release.TestOperatorPatternsCannotHangThePipeline`, `release.FuzzProfileTerms` |
| A release whose quality is only half-known is classified DOWN, so it cannot clear a cutoff by being vague | `release.TestHalfKnownQualityRoundsDown` |
| XML external entities are not resolved, and the billion-laughs expansion does not expand | `indexer.TestXXEIsImpossible`, `indexer.TestBillionLaughsIsImpossible` — real payloads, not a claim |
| An indexer cannot exhaust memory: the size cap applies to the decompressed stream, not to a Content-Length it controls | `indexer.TestTheSizeCapAppliesToTheDecompressedStream`, `TestOversizedResponsesAreRefused` |
| A result whose download URL points at loopback, cloud metadata or a non-HTTP scheme is dropped | `indexer.TestResultsWithUnsafeDownloadURLsAreDropped` |
| A redirect target is validated, because the far end chose it after the request | `indexer.TestRedirectTargetsAreValidated` |
| A tracker page read through its Cardigann definition is hostile input like a feed: the same size and row caps, every link resolved and checked by the same rule, a search path naming another private host refused before it is asked; the definition's patterns are RE2 and its templates can call only `join` and `re_replace` (ADR-0058) | `indexer.TestAPublicTrackerIsSearchedFromItsDefinition`, `indexer.TestADefinitionIsCheckedWhenSaved` |
| A tracker's username, password or cookie is sealed under its indexer's own context, never returned or listed, re-sealed by `-rotate-key`, and sent only to the tracker's own scheme, host and port — a definition's path or a page's form pointing anywhere else is refused before anything is sent; the session's cookies live in memory only (ADR-0059) | `indexer.TestCardigannSettingsAreSealed`, `indexer.TestCredentialsGoOnlyToTheTrackersOwnAddress`, `api.TestCardigannSettingsNeverComeBackOverHTTP`, `main.TestTheMasterKeyIsRotated` |
| An indexer API key is sealed context-bound and cannot be relocated between indexers | `indexer.TestASealedKeyCannotBeMovedBetweenIndexers` |
| An indexer API key never leaves over HTTP, and is never decrypted on the admin path | `api.TestIndexerAPIKeyNeverComesBackOverHTTP`, `indexer.TestListNeverCarriesTheAPIKey` |
| An indexer API key never reaches a log line or an error message | `indexer.TestErrorsNeverCarryTheAPIKey`, `TestRedactURLRemovesEverySecretParameter` |
| An acquisition package cannot build its own HTTP transport | `egress.TestNoPackageDialsDirectly` — `http.Transport` is banned outright there |
| Turning egress enforcement off lifts the tunnel gate and nothing else | `egress.TestEnforcementOffDoesNotLiftEveryOtherControl` |
| The admin surface says in words when egress is not being enforced | `api.TestEgressStatusSaysWhenItIsNotEnforcing` |
| A search never hands the client a download URL to hand back | `api.TestSearchResultsCarryNoDownloadURL`, `api.TestAGrabTicketIsOpaqueOnTheWire` |
| A grab takes a sealed ticket, never a URL, so the client cannot choose what gets downloaded | `api.TestAGrabRefusesToTakeAURL` — including a **valid** ticket with an extra `url` field, which is the case that actually tests it |
| A grab ticket cannot be tampered with, replayed by another account, or reused after expiry | `search.TestATamperedTicketIsRefused` (every byte position), `TestATicketCannotBeUsedByAnotherAccount`, `TestAnExpiredTicketIsNamedAsExpired` |
| A grab ticket does not outlive an operator disabling the indexer it came from | `api.TestATicketDoesNotOutliveItsIndexerBeingDisabled` — the indexer is re-resolved against current state on every use |
| A grab is audited on failure as well as success | `api.TestAGrabIsAudited`, `api.TestAFailedGrabIsAuditedToo` |
| A grab's audit line names the release but never the download URL that carries the API key | `api.TestAGrabIsAudited` |
| Every interactive search is judged by the instance's default quality profile unless the person chooses another or none, and the answer says which judged it | `api.TestEverySearchIsJudgedByTheDefaultUnlessAnotherIsChosen`, `api.TestTheFilmSearchReadsTheProfileByTheSameRule`, `api.TestTheEpisodeSearchReadsTheProfileByTheSameRule` (ADR-0027) |
| Only an administrator chooses the default: the route is hidden from everyone else, a refused change moves nothing, and every change is audited with what it was | `release.TestChoosingTheDefaultNeedsSystemSettings`, `api.TestChoosingTheDefaultIsAnAdministratorsAndIsAudited` |
| The default cannot be deleted out from under the searches, and the database holds at most one | `release.TestTheDefaultProfileCannotBeDeleted`, `release.TestTheDatabaseRefusesASecondDefault` |
| Automatic acquisition is off unless the configuration turns it on, and cannot be on with the download engine off | `config.TestAutomaticAcquisitionIsOffByDefault`, `config.TestAutomaticAcquisitionSettingsThatCannotWorkAreRefused` — mutation-verified (ADR-0030) |
| It acts on exactly what the Wanted screen lists, and a person who unmonitors an item while a pass runs has the last word | `acquire.TestTheWantedListIsTheWantedScreens` (a differential test against the screen's own queries), `acquire.TestAPersonsChangeDuringAPassWins` — mutation-verified |
| It grabs only what the default profile accepts, with seeders, and never a release that was ever in the queue — known by its hash in the feed or only once fetched | `acquire.TestDeadAndRefusedReleasesAreNotGrabbed`, `acquire.TestTheQueueIsTheBlocklist`, `acquire.TestNoDefaultProfileMeansNothingIsFetched` — mutation-verified, and against the binary |
| One download per wanted item: nothing more while one is under way or failed to import; a double episode only when both its episodes are wanted; the two passes never both grab | `acquire.TestOneDownloadPerWantedItem`, `acquire.TestWhatCountsAsInFlight`, `acquire.TestADoubleEpisodeIsGrabbedOnceAndHoldsBoth`, `acquire.TestTheTwoPassesNeverGrabTwiceForOneItem` — mutation-verified |
| A release whose name fits two titles in the library, or two wanted titles, is grabbed for neither; nor is one named in a language, which may be a dub | `acquire.TestANameThatFitsTwoTitlesIsGrabbedForNeither`, `acquire.TestANameTwoWantedTitlesAnswerToIsGrabbedForNeither`, `acquire.TestAReleaseNamedInALanguageIsAPersonsChoice` — mutation-verified |
| It asks no indexer anything while egress is enforced and the tunnel is not verified | `acquire.TestAClosedGateAsksNoIndexer` — mutation-verified, and against the binary with the tunnel down |
| Its request budget holds: one feed request per indexer a pass, a few searches a pass, a back-off on nothing found, an hour on failure, a failed fetch left alone for six hours, and a ceiling on grabs | `acquire.TestARecentReleaseOfAWantedEpisodeIsGrabbed`, `acquire.TestTheSearchPassIsBudgetedAndBacksOff`, `acquire.TestAFailedSearchIsRetriedInAnHour`, `acquire.TestAFailedFetchIsNotRepeatedEveryPass`, `acquire.TestAPassGrabsNoMoreThanItsLimit`, `acquire.TestAlternativeTitlesAreUsedCachedAndBudgeted` — mutation-verified |
| It runs on browse, search and the queue, and nothing else — a pass refuses any other authority — and every grab it makes is audited as `system:acquire`, with no link | `authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant`, `acquire.TestAPassNeedsTheTasksAuthority`, `acquire.TestARecentReleaseOfAWantedEpisodeIsGrabbed`, `acquire.TestAFailedFetchIsNotRepeatedEveryPass` — mutation-verified |
| Taking a film off the Wanted list is library editing, and the queue tells an automatic download from a person's | `importer.TestAFilmCanBeKeptWithoutBeingWanted`, `api.TestAFilmIsUnmonitoredThroughTheAPI`, `api.TestTheQueueSaysWhichDownloadsAreAutomatic` — mutation-verified |
| The engine cannot be reached with a caller-supplied magnet: only payloads the server fetched | The `DownloadEngine` interface exposes no URL-taking add; `api.TestAGrabRefusesToTakeAURL` |
| Every outbound peer connection goes through the guard — ours is the **only** dialer, not merely one of them | `download.TestAClosedGateStopsPeerConnections` — a real transfer on loopback with the gate shut. Flipping `DialForPeerConns` back makes it fail in 0.11s |
| DHT and uTP cannot leak under a proxy that cannot carry UDP — with enforcement on or off | `download.ConfigFor` forces them off; `download.TestASOCKS5ProfileForcesDHTAndUTPOff`, `download.TestAProxyProfileForcesUDPOffWithoutEnforcement` (found live in 7a: with enforcement off they stayed on), `download.TestTheNamespaceGuardKeepsDHTAndUTP` |
| Tracker announces go through the guard like peer connections | `download.TestTrackerAnnouncesGoThroughTheGuard` — found live in 7a: the library's tracker dialer was never set, so announces left by the host's own route |
| No UDP tracker is used beside a proxy that will not carry it, from a .torrent or a magnet | `download.TestUDPTrackersAreRefusedUnderAProxy`, `download.TestAProxyThatRefusesUDPLeavesUDPTrackersOff` |
| Under a proxy that relays UDP, UDP trackers go through it, their names resolved by the proxy and never here; the kill switch and a blocked profile stop UDP as they stop TCP | `egress.TestAnAliasReachesTheProxyAsTheTrackersName`, `egress.TestTheKillSwitchStopsUDPToo`, `egress.TestABlockedProfileOpensNoUDPSocket`, `download.TestUDPTrackersGoThroughAProxyThatRelaysUDP` — mutation-verified (ADR-0067) |
| A blocked download profile sends no UDP either: DHT, uTP and incoming connections are off | `download.TestABlockedProfileTurnsUDPOff` — found on the running binary in 7a's review: blocked stopped TCP at the guard while DHT and uTP, which have sockets of their own, stayed on |
| A title's id is never used again, so a download, request or issue that outlives its title cannot be taken for another's; deleting a title stops the downloads aimed at it | `db.TestATitlesIDIsNeverUsedAgain`, `api.TestDeletingATitleStopsTheDownloadsAimedAtIt` — mutation-verified (ADR-0066; found on the first real instance, where a deleted series' season download said it was for a film) |
| A stored proxy that cannot be read blocks what it may carry rather than leaving it direct | `egressproxy.TestAnUnreadableProxyBlocksWhatItMightCarry` — mutation-verified, and on the running binary with a password sealed under another key |
| A torrent's declared name — attacker-chosen — never reaches a filesystem path | `download.TestAPathTraversalNameNeverReachesAPath` |
| A data path cannot be asked for outside the data directory, even by a caller passing a traversal | `download.TestADataPathCannotBeAskedForOutsideTheDataDirectory` — `filepath.Join` *cleans*, so this is refused before the join |
| A queue identifier that is not an info hash never reaches the engine | `api.TestTheQueueRefusesAnythingThatIsNotAnInfoHash` — asserts 400 specifically, and that the engine was not called at all |
| Removing from the queue stops the transfer and never unlinks bytes | `download.Manager.Remove` has no delete path; `authz.TestManagerWithQueueAccessCannotDestroyMediaBytes` |
| Grabbing requires the queue permission, not merely search | `api.TestGrabbingRequiresTheQueuePermissionNotJustSearch` |
| The indexer client has exactly one URL validator, so the pre-flight and redirect checks cannot drift | `indexer.TestTheClientHasExactlyOneURLValidator` |
| A transfer running with no queue row is surfaced as `unrecorded`, not smoothed over | `api.TestATransferWithNoQueueRowIsShownAsUnrecorded` |
| A queue that cannot be read is an error, not an empty list | `api.TestAnUnreadableQueueIsAnErrorNotAnEmptyList` |
| A restart does not forgive seeding already done | `download.TestSeedingTotalsSurviveARestartedUploadCounter` |
| No library path escapes its root — not by `..`, not by an absolute path, not by a symlink planted inside the tree | `library.TestNoPathEscapesItsRoot` — 8 escape shapes × 8 operations, against real symlinks. Containment is `os.Root`/`openat2(RESOLVE_BENEATH)`: the **kernel**, per component, at the moment of use |
| That choice is not cosmetic | `library.TestTheVaultRefusesWhatTheObviousImplementationAllows` — a differential test keeping the usual Join+prefix version beside ours. It reads `/etc/passwd` through a planted link; the vault refuses all four. The test also fails if the naive version ever contains everything, since the comparison would then be worthless |
| Removing a symlink unlinks the link, never its target | `library.TestRemovingASymlinkDoesNotFollowIt` — otherwise "clean up this import" becomes "delete /etc" |
| Nothing in the library packages can write outside a Vault | `library.TestNothingWritesOutsideAVault` — fails the build on `os.Create`, `os.Rename`, `os.Remove`, `os.Link`, and on `filepath.Join`. Two files are exempt, both documented |
| Every destructive vault method checks an effect | `library.TestEveryDestructiveMethodChecksAnEffect` — parses the source, so a new method added without the check cannot slip through review |
| A Manager cannot destroy media bytes or repoint paths, whatever route they reach | `library.TestAManagerCannotDestroyBytesOrMovePathsThroughTheVault` — the check is in the filesystem layer, not a handler |
| An anonymous context destroys nothing | `library.TestAnAnonymousContextDestroysNothing` |
| A whole library cannot be deleted in one call | `library.TestTheRootItselfCannotBeRemoved` — `RemoveAll(".")` is refused outright |
| A stranger-supplied name always lands inside the root and is always usable | `library.TestASanitisedNameAlwaysLandsInsideTheRoot`, `library.FuzzSafeComponent` (2.1M executions) |
| Root folders cannot nest, or overlap the download directory | `library.TestRootFoldersMayNotNest`, `library.TestARootMayNotOverlapTheDownloadDirectory` — both verified against the binary |
| A root folder added through a symlink cannot be added twice under two names | `library.TestTheSameDirectoryCannotBeAddedTwiceUnderDifferentNames` — otherwise the nesting check is defeated |
| Writability is proven by writing, not by reading mode bits | `library.TestAReadOnlyRootIsRefused` — **skips when the test runner is root**, which it says out loud rather than passing vacuously |
| Removing a root folder never deletes files | `library.TestDeletingARootFolderLeavesTheFilesAlone`, `api.TestDeletingARootFolderSaysTheFilesAreStillThere` |
| Where the library lives is audited, by path and not only by id | `api.TestRootFolderChangesAreAudited` |
| A root folder still holding library records cannot be deleted | `library.ErrRootInUse` — cascading would erase the record of hundreds of files while leaving every one on disk |
| Only a video container can ever be imported — an allowlist, never a denylist | `importer.TestOnlyAllowlistedContainersCanBeChosen`, `importer.FuzzSelectNeverChoosesANonContainer` (1.2M executions) |
| An executable in a download never reaches the library | `importer.TestAnExecutableNeverReachesTheLibrary` |
| **A download cannot name a file outside itself** | `importer.TestADownloadCannotNameAFileOutsideItself` — the torrent's file list is written by the uploader, and `filepath.Join` CLEANS, so `../../../../etc/shadow` becomes `/etc/shadow`. Found by `library.TestNothingWritesOutsideAVault` firing on the line, not by review. With the check removed, a file from outside is hardlinked into the library within one test run |
| Sample detection does not discard real films | `importer.TestAFilmWhoseTitleContainsASampleWordSurvives` — *Free Samples (2012)*, *Trailer Park Boys: The Movie*, *Resampled* |
| A hostile release title cannot escape the library layout | `importer.TestAHostileTitleCannotEscapeTheLayout`, `importer.TestAThinParseIsRefusedRatherThanFiledBlind` |
| An upgrade is reversible: the file it replaces moves to trash, not to nowhere | `importer.TestAnUpgradeIsReversible` |
| The importer holds browse and path-mutation and **nothing else** | `authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant` — walks `AllPermissions` per task, so a permission added by accident fails the build |
| The importer cannot unlink a media file | `authz.TestTheImporterCannotDestroyMediaBytes` — which is what makes the trash design load-bearing rather than a nicety |
| Background authority can only be minted where scheduled work is registered | `authz.TestOnlySchedulingCodeCanMintASystemPrincipal` — reads every package's source; a runtime check cannot prove a handler does not call it |
| Importing never consumes the download, so seeding continues | `importer.TestImportingNeverConsumesTheDownload` — verified against the binary: library and download share one inode |
| **A scan changes nothing on disk** | `importer.TestAScanNeverChangesAnythingOnDisk` — every path, size and content hash compared before and after. Software that "organises on scan" is how people lose libraries |
| **A library that appears to have vanished is refused, not recorded** | `importer.TestAVanishedLibraryIsRefusedRatherThanRecorded`, `api.TestAVanishedLibraryIsRefusedOverHTTPWithAnExplanation` — a failed mount makes a root look empty, and acting on it would erase the record of a whole disk in one pass |
| A few genuine deletions are still reported rather than swallowed by that guard | `importer.TestAFewDeletedFilesAreReportedNotRefused` |
| A scan never indexes through a symlink out of the root | `importer.TestAScanDoesNotFollowSymlinksOutOfTheRoot` — `fs.WalkDir` over an `os.Root` reports a link and does not descend; verified empirically before being relied on |
| A scan cannot be pointed at an arbitrary path | The endpoint takes a configured root folder's **id**; it carries no path at all |
| Asking the instance to walk an operator's disks is audited | `api.TestScansAreAudited`, and gated on root-folder administration rather than browse (`api.TestScanningIsInvisibleToNonAdmins`) |
| The library listing does not leak the directory layout | `api.TestTheLibraryListingDoesNotLeakAbsolutePaths` — paths are relative to a root, and `Store.filesInRoot` is unexported |
| **Deleting media never unlinks anything** | `importer.TestDeletingAnItemTrashesItRatherThanUnlinkingIt`, `api.TestDeletingMediaTrashesItAndSaysSo` — files move to the root's trash and the retention window is the undo. There is deliberately no "delete and purge now"; one file is deleted the same way (`importer.TestDeletingOneFileTrashesItAndKeepsTheTitle`), and unlinking one trashed file now is its own act, from the trash only, through the vault's destroy check (`importer.TestOneTrashedFileCanBePurgedNow`, ADR-0053) |
| The undo works, not just in principle | `importer.TestATrashedFileCanBeRestored`, `api.TestATrashedFileCanBeRestoredOverHTTP` |
| Only the purge principal can unlink a media file unnamed by a person | `importer.TestOnlyThePurgePrincipalCanPurge` — the importer, the scanner and anonymous all fail |
| Each background task holds exactly its own grant, and each is smaller than a shared set | `authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant` — walks `AllPermissions` per task, and fails the build if a declared task is unasserted |
| An unknown background task gets no authority at all | `authz.TestAnUnknownTaskGetsNothing` — a typo must produce work that cannot act, not work that inherits somebody else's grant |
| A Manager cannot delete media, over HTTP or otherwise | `api.TestAManagerCannotDeleteMediaOverHTTP` — the route is invisible and the file survives |
| No script in either bundle builds markup from a string | `web.TestNoScriptBuildsMarkupFromStrings` — every node comes from `document.createElement`, so a release name a stranger wrote cannot become markup |
| Every nav entry has a section, and every section a nav entry | `web.TestEveryNavEntryHasASectionAndEverySectionHasANavEntry` — a mismatch shows an empty page or leaves dead markup, and neither errors |
| Every data view clears or reloads when shown | `web.TestEveryDataViewHasALoader` — this found the Search view showing a previous search's results as current, with Grab buttons whose tickets were expiring |
| The indexer API key never survives in the page | The Chromium run asserts the field is cleared on submit and the key appears nowhere in the DOM |
| The UI renders with zero CSP violations | The Chromium run collects every CSP refusal and script error and fails on any |
| A restore cannot become "move any library file somewhere else" | `api.TestRestoreOnlyAcceptsPathsInTheTrash`, `importer.TestRestoringCannotEscapeTheRoot` — containment is not the same as the operation being the right one |
| A zero retention is refused, not honoured | `importer.TestAZeroRetentionIsRefused` — it would delete files the instant they were trashed |
| A new Vault method that writes without an authority decision fails the build | `library.TestEveryDestructiveMethodChecksAnEffect` — verified by adding a plausible `Tidy()` and watching it fail |
| A partial search is reported as partial, with the per-indexer reason | `api.TestAPartialSearchSaysSoInWordsAndNotOnlyAsABoolean` |
| The security lint accepts the deployment the documentation recommends, and still refuses direct egress with nothing enforcing it | `config.TestTheRecommendedEgressConfigurationPassesTheLint`, `TestDirectWithNoEnforcementIsStillRefused` |
| The download engine refuses to start unless it can prove its traffic leaves through the tunnel | `runDownloader` exits non-zero; verified against the binary, and `egress.TestVerifyRefusesWhenTheInterfaceIsWrong` |
| No egress mode falls back to a direct connection when the tunnel is down | `egress.TestNoFallbackWhenUnhealthy` — every mode, against a listener that would have accepted |
| No acquisition package can reach the network outside the guard | `egress.TestNoPackageDialsDirectly` — fails the build on `net.Dial`, `http.Get`, `http.DefaultClient` |
| A subsystem with no egress profile is blocked, not direct | `egress.TestUnknownProfileIsBlockedNotDirect` |
| Egress health starts down and anything inconclusive stays down | `egress.TestHealthStartsDown`, `TestProbeReportsUnhealthyWhenRoutingCannotBeConfirmed` |
| Only the health probe may bypass the kill switch, and only the health gate | `egress.TestOnlyTheProbeBypassesTheGate`, `TestProbeDialerBypassesTheGateButNotThePolicy` |
| SOCKS5 hands the hostname to the proxy rather than resolving it locally | `egress.TestSOCKS5SendsTheHostnameRatherThanResolvingIt` |
| A rejected proxy credential never reaches the log | `egress.TestSOCKS5RejectedCredentialsFailWithoutLeakingThem` |
| Egress policy cannot be changed over HTTP, except one SOCKS5 proxy (ADR-0065) | `api.TestEgressPolicyCannotBeChangedOverHTTP` — 409, permanently |
| The proxy is set only with the password and a fresh authenticator code, never a recovery code or a replayed one, and failures count toward lockout | `identity.TestReauthenticationNeedsBothThePasswordAndACode`, `TestReauthenticationRefusesAReplayedCodeAndRecoveryCodes`, `TestReauthenticationFailuresCountTowardLockout`, `api.TestAProxyIsSavedOnlyWithThePasswordAndACode`, `api.TestTheProxyRouteIsHiddenAndRefusesAPITokens` — mutation-verified |
| A proxy change is audited without its password and reaches Discord whatever the categories say | `api.TestAProxyChangeIsAuditedWithoutItsPassword`, `notify.TestAProxyChangeIsSentWhateverTheCategories` — mutation-verified |
| A stored proxy the lint refuses blocks its traffic rather than going direct or stopping the boot; a ticked profile is never left direct | `egressproxy.TestAnOverlayTheLintRefusesBlocksInsteadOfBricking`, `TestATickedProfileIsNeverLeftDirect`, `TestValidateRefusesWhatTheLintRefuses` — mutation-verified |
| The proxy password is sealed, rotated with the master key, never shown, and not carried to another proxy or account | `egressproxy.TestAProxyIsStoredSealedAndLoaded`, `TestABlankPasswordKeepsTheStoredOneForTheSameProxy`, `main.TestTheMasterKeyIsRotated` |
| The proxy password never leaves the process | `egress.TestProfilesNeverExposeTheProxyPassword`, `api.TestEgressStatusNeverReturnsTheProxyPassword` |
| Addresses hostile input must not reach are refused on the profiles that see it | `egress.TestRestrictedRangesAreRecognised`, `TestDenyPrivateRefusesLoopback` |
| An indexer on the operator's network is reachable at its own configured address and nowhere else private — not another port on the same machine, not another indexer's address, not a redirect away from it | `egress.TestTheAllowedDestinationAndOnlyItIsReachable`, `indexer.TestAnIndexerOnTheOperatorsNetworkWorks`, `indexer.TestALinkToAnotherPrivateAddressIsStillRefused`, `indexer.TestARedirectAwayFromTheIndexerIsRefused`, `indexer.TestOneIndexersAllowanceIsNotAnothers` — mutation-verified (ADR-0024) |
| Link-local is refused even as an indexer's own address, at save and at dial | `indexer.TestAnIndexerAddressIsCheckedWhenSaved`, `egress.TestLinkLocalIsRefusedEvenWhenItIsTheConfiguredAddress`, `indexer.TestTheIndexersOwnAddressIsStillNotLinkLocal` — mutation-verified |
| The exemption can be created in one place only | `egress.TestOnlyTheIndexerClientMayAllowAPrivateDestination` — reads every file in the module |
| The CSP permits no inline or eval execution | `api.TestSecurityHeadersArePresent` |
| A panic does not leak its value to the client | `api.TestPanicIsContainedAndNotLeaked` |
| The server refuses to boot in an insecure configuration | 11 tests in `config` |
| A first-run wizard closes permanently once any account exists, and creates no second admin | `api.TestSetupIsUnreachableOnceAnAccountExists` |
| An account cannot act before enrolling an authenticator — including the first Admin | `api.TestAdminCannotActBeforeEnrolling` (end to end, real database) |
| A pending applicant cannot log in | `api.TestSignupApprovalAndFirstLogin` |
| Signup does not reveal whether an address is registered | `api.TestSignupIsNotAnEnumerationOracle` — asserts byte-identical bodies |
| An unknown username and a wrong password are indistinguishable | `api.TestLoginFailuresAreIndistinguishable` |
| Repeated failures are throttled per account and per IP, and the throttle applies even to a correct password | `api.TestLoginThrottlingEngages` |
| A TOTP code cannot be replayed inside its 30-second step | `api.TestTOTPCodeCannotBeReplayed` |
| Suspension kills live sessions immediately, with no clock advance | `api.TestSuspensionKillsLiveSessionsImmediately` |
| A superseded session secret is treated as theft and revokes the session | `api.TestSupersededSessionSecretIsTreatedAsReuse` |
| A Manager cannot approve into Admin **through the HTTP stack**, not merely in the engine | `api.TestManagerCannotApproveIntoAdminOverHTTP` |
| A privileged field smuggled into a signup body is rejected, not ignored | `api.TestSmuggledPrivilegedFieldsAreRejected` |
| Logout revokes the session; an idle session expires | `api.TestLogoutRevokesTheSession`, `api.TestSessionExpiresOnIdleTimeout` |
| **A user cannot revoke another user's session, even holding its ID** | `api.TestUserCannotRevokeAnotherUsersSession` — ownership is a WHERE-clause predicate, not a preceding lookup |
| An invite is single-use, expiring, and dies if revoked | `api.TestInviteIsSingleUse`, `api.TestExpiredAndRevokedInvitesAreRefused` |
| A Manager cannot issue an Admin invite — an invite is an approval made in advance | `api.TestManagerCannotIssueAnAdminInvite` |
| Suspending a user revokes the invites they issued | `api.TestSuspendingAnIssuerRevokesTheirOutstandingInvites` |
| Invite codes are never readable back, including by their issuer | `api.TestInviteListNeverContainsCodes` |
| Password-reset initiation reveals nothing, including that delivery is unimplemented | `api.TestResetInitiateIsNotAnEnumerationOracle` |
| **A password reset does not bypass the authenticator** | `api.TestResetDoesNotBypassMFA` |
| A reset token is single-use and expires in 30 minutes | `api.TestResetTokenIsSingleUseAndExpires` |
| Completing a reset revokes every live session | `api.TestAdminMintedResetChangesThePassword` |
| A Manager cannot mint a password reset for an Admin | `api.TestManagerCannotMintAResetForAnAdmin` |
| Changing a password requires the current one, and revokes other sessions only | `api.TestChangePasswordRequiresTheCurrentOne`, `api.TestChangePasswordRevokesOtherSessionsOnly` |
| **A token's scope can never exceed its issuer's permissions** | `api.TestTokenScopeCannotExceedTheIssuer` |
| **A token narrows automatically when its owner is demoted** | `api.TestTokenNarrowsWhenItsOwnerIsDemoted` — scope is re-intersected with the current role on every use |
| **A token cannot reach credential management, whatever its scope** | `api.TestTokenCannotReachCredentialManagement` — nine routes, tested with an all-permissions token |
| A token dies with its owner's access | `api.TestSuspensionKillsAPITokens` |
| A revoked token stops immediately; an expired one is refused | `api.TestRevokedTokenStopsImmediately`, `api.TestExpiredTokenIsRefused` |
| A user cannot revoke another user's token | `api.TestUserCannotRevokeAnotherUsersToken` |
| A bad Bearer token never falls back to the session cookie | `api.TestBadBearerTokenDoesNotFallBackToTheCookie` |
| A token principal is marked as one, so the session-only guard can fire | `api.TestTokenPrincipalIsMarkedAsAToken` |
| Metric label values cannot inject a fabricated series | `metrics.TestLabelValuesAreEscaped` |
| A metrics bug cannot break a request path | `metrics.TestWrongLabelCountDoesNotPanic` |
| The egress-proxy health gauge starts DOWN, not absent | `metrics.TestProxyHealthStartsDown` |
| A scheduled task never runs concurrently with itself | `tasks.TestTaskNeverRunsConcurrentlyWithItself` |
| A task panic is contained and recorded as a failure | `tasks.TestPanicIsContainedAndRecordedAsFailure` |
| A recovery code is single-use, and regenerating retires the whole old set | `api.TestRecoveryCodeIsSingleUse`, `api.TestRegeneratingRecoveryCodesInvalidatesTheOldSet` |

Run them: `make acceptance`, or `make test` for everything.

---

## Cryptography

| Purpose | Algorithm | Parameters |
|---|---|---|
| Password hashing | Argon2id | m=19456 KiB, t=2, p=1, 16-byte salt, 32-byte key (OWASP's second recommended configuration) |
| Recovery codes | Argon2id | Same. Single-use, hashed like passwords |
| Secrets at rest | AES-256-GCM | Random 96-bit nonce per encryption; the storage context is bound as additional authenticated data |
| Second factor | TOTP, RFC 6238 | SHA-1, 6 digits, 30-second step, ±1 step tolerance, 160-bit secret |
| Session and CSRF tokens | `crypto/rand` | 128 bits minimum |
| Backups | age v1 (`filippo.io/age` v1.3.2), scrypt recipient | scrypt work factor 2^15 wrapping a random file key; the payload ChaCha20-Poly1305 in authenticated 64 KiB chunks. Passphrase = unpadded base64url of HKDF-SHA256(master key, no salt, info `cmediastack backup v1`), 32 bytes — see *Backups* |

Argon2id parameters are stored inside each hash, so raising them later does not
invalidate existing credentials; `identity.NeedsRehash` reports which
credentials should be re-derived on next successful login.

TOTP is implemented in-tree rather than taken as a dependency. That is a
deliberate exception to the project's "wrap mature libraries" rule: RFC 4226
truncation over an HMAC is about thirty lines, it sits on the most
security-critical path in the application, and it is verified against the RFC's
own published test vectors. Removing a third-party dependency from the
authentication path is worth more here than the lines saved.

---

## The master key

`CMS_MASTER_KEY` is 32 random bytes, base64 encoded. Generate one with
`make genkey`.

- It is read from the environment only. It is never read from the config file,
  never written to the database, and never logged.
- **Losing it makes every stored credential unrecoverable.** Indexer API keys,
  proxy passwords and OIDC client secrets are all sealed under it.
- **Back it up separately from the database.** A backup containing both the
  database and the key is a backup of plaintext secrets. The built-in backups
  are encrypted to a passphrase derived from this key, so without it they are
  unreadable — which is the point, and also why losing it loses them.
- The server refuses to boot if the key is missing, not base64, or not exactly
  32 bytes.

### Rotation

`cmediastack -rotate-key`, on the host with the server stopped (ADR-0054). The
old key is `CMS_MASTER_KEY`, the new one `CMS_MASTER_KEY_NEW`, both from the
environment. Every stored secret — authenticator secrets, indexer API keys, the
metadata token, the Discord webhook — is opened under the old key with its own
context and re-sealed under the new one in one transaction; one that will not
open changes nothing. The audit log records the rotation and the counts, never
a key (`main.TestTheMasterKeyIsRotated`,
`main.TestARotationThatCannotOpenEverythingChangesNothing`), and a test fails the
build for any sealed value the rotation does not cover
(`main.TestEverySealedValueIsRotated`). Grab tickets and signup challenges in
flight are refused afterwards and simply asked for again.

**Keep the old key with the old backups.** They are encrypted under a passphrase
derived from it and are not rewritten; `-verify-backup` and `-restore-backup`
use whichever key is in `CMS_MASTER_KEY`.

---

## Backups

The database — everything the instance knows, and none of the media — is
backed up once a day by default, encrypted, and kept for a week, never fewer
than the newest three (ADR-0029). What protects a backup, and what does not:

- **Encrypted, and only readable with the master key.** Each backup is an age
  file whose passphrase is derived from the master key by HKDF. The key is in no
  backup. The passphrase is not the key: it can be given to the standard `age`
  tool without giving away the key that seals every stored credential. It is
  written nowhere and nothing prints it; docs/RUNBOOK.md has the recipe for
  deriving it with `openssl` or Python on a machine where this software is gone.
- **Not forgeable.** A passphrase recipient means only a holder of the
  passphrase can write a file it opens. With a public-key recipient anyone who
  knew the public half could plant a "backup" — an administrator with a
  password they chose — that restored cleanly.
- **Checked before it is kept.** The snapshot must pass SQLite's integrity and
  foreign-key checks and carry exactly this build's migrations; the encrypted
  file is then read back and decrypted, and must match both what was written and
  the snapshot that was checked. Only then does it get a backup's name.
- **The plaintext stays beside the database.** The snapshot is written `0600`
  into the database's own directory — never the backup directory, which may be
  a NAS — and removed when the backup finishes. The directory is `0700`, each
  backup `0600`.
- **No route serves a backup, and none restores one**, and a test fails the
  build if either appears. An administrator can take one and list them; carrying
  one off, or rolling the instance back to one, needs the host. The routes are
  hidden from everyone without `admin.system`, and taking a backup is audited
  with who, from where, and the file's SHA-256.
- **Pruning goes by age and only on the schedule.** Taking backups — however
  many, by whoever holds an administrator's session — deletes none. Only
  regular files named as backups are ever deleted.
- **A restore is fenced.** `cmediastack -restore-backup FILE -restore-to PATH`
  writes a new file, never over one, and before handing it over ends every
  session in it, revokes every API token, and writes the restore into its audit
  log. Otherwise a session stolen and ended since the backup, or a token revoked
  since, would work again. What it cannot undo: accounts suspended since the
  backup are active in it, and passwords changed since are the old ones. The
  command says so.
- **A named backup directory is never created**, because a missing one is more
  often an unmounted disk, and creating it would put the backups on this host's
  disk while the operator believed they were elsewhere. The admin screen says
  when backups share the database's filesystem.

What a backup holds is everything the database does: accounts, password hashes,
authenticator secrets and indexer keys (both sealed under the master key), email
addresses, what people watched and asked for, and the audit log. Copy it off the
host — that is what it is for — and treat the copy accordingly.

---

## The anonymous surface

Twelve routes, listed literally in `internal/api/allowlist.go`:

```
GET  /login                         POST /api/v1/auth/login
POST /api/v1/auth/login/mfa         GET  /signup
POST /api/v1/auth/signup            GET  /reset
POST /api/v1/auth/reset/initiate    POST /api/v1/auth/reset/complete
GET  /assets/auth/                  GET  /healthz
GET  /setup                         POST /api/v1/setup
```

The two setup routes are first-run only: both handlers check the account count
on every request and return 404 once any account exists, so the wizard closes
itself the moment the first administrator is created.

**Until then, whoever reaches `/setup` first becomes the administrator.** That
window is the operator's to close: finish the wizard before the instance is
reachable from the internet. docs/RUNBOOK.md does the first run over an SSH
tunnel for exactly this reason.

Everything else requires a session. That includes the application shell and its
JavaScript bundle, which are served from an authenticated route so that an
anonymous scanner cannot enumerate the API client.

The auth pages load a **separate, self-contained bundle** for exactly this
reason, and the separation is asserted rather than assumed:
`web.TestAnonymousBundleNamesNoAuthenticatedEndpoint` scans everything under
`assets/auth/` for any `/api/` path and fails on anything outside the nine
endpoints reachable before a principal can act. Splitting the bundles achieves
nothing on its own; that test is the enforcement.

**One route answers a denial with a redirect rather than a 404:** `GET /{$}`,
the root, sends a caller with no usable session to `/login` and an un-enrolled
one to `/enroll`. §2 promises a visitor is shown a login screen, and the root is
where a visitor arrives; the disclosure is nil, since every origin has a root
and `/login` is anonymous already. The destination is a constant in the source —
no `?next=`, no `Referer`, nothing caller-supplied, because an open redirect on
a login flow is a phishing primitive. A suspended account receives the identical
response, or the root would become an account-existence oracle. `register()`
panics if a second route is marked this way. [ADR-0012](docs/adr/0012-root-redirect-carve-out.md).

`/healthz` returns the string `ok` and nothing else — no version, no counts, no
user information — and binds to the management listener, not the public one.

Adding a thirteenth entry requires editing the literal allowlist, which is a
reviewable line in a diff, and it will fail `TestAllowlistAndRegistrationsAgree`
until the route registration agrees with it.

---

## Authorization model

Two properties that are unusual enough to call out:

**Permissions are defined over effects, not routes.** "Delete media files:
Admin only" is meaningless if a Manager can reach the same unlink by removing a
torrent with its data, repointing a root folder, or renaming a file into a
void. `authz.RequireEffect` is called at the point the effect occurs, inside
the filesystem layer, so every route that reaches an effect is checked whether
or not anyone remembered to check it at the route.

**Unauthorized administration routes return 404, not 403.** A 403 confirms the
route exists. The real denial, with actor, route, source IP and reason, goes to
the audit log — so an operator can still tell an attack from a bug.

**Those denials have a ceiling, because anybody can cause one.** A request for a
protected route with no session writes a row, so without a limit a scanner
writes one per request: the disk fills, every backup carries it, and the audit
screen shows nothing else. Anonymous denials are written one by one up to 20 an
hour from one address and 120 an hour from every address together; a signed-in
account's up to 60 an hour, counted apart so an anonymous flood can neither hide
an account's nor lift its ceiling. The rest are counted, and when the hour is
over one `authz.denied.suppressed` line by `system:audit` says how many and from
where. The worst a flood can add is 121 rows an hour from any number of
addresses; `cms_authz_denials_total` still counts every denial (ADR-0031).

**Denials decided inside a service are audited too, not only route-level ones.**
This is the class that matters: reaching an administration route already
required the permission, so every *interesting* refusal — "tried to promote
themselves", "tried to suspend a superior" — is decided later, by
`authz.CanModifyUser` or `authz.CanAssignRole`. Route-level logging alone sees
none of them. Refusals on `user.role.change`, `user.role.assign`,
`user.reactivate` and `user.suspend` are recorded with actor, source IP and
user agent (`api.TestARefusedEscalationIsRecorded`).

**What an account may see of the library is a scope, applied where the rows are
read** ([ADR-0037](docs/adr/0037-libraries-and-rating-ceilings.md)). A library is a root folder.
An account sees every library or the ones it is granted, and with a rating
ceiling only titles rated at or below it — **an unrated title is hidden from
every account with a ceiling**. The filter is part of the SQL of every read made
for a person: the title list, a title and its files, a series' seasons and
episodes, the Wanted list, every playback route, a search for a title and so a
grab from it, the monitoring, profile and rating switches, the identification
review, the root folders a person may browse, a request's library item, and a
poster to anyone without `library.edit`. **A title out of scope answers exactly
as an absent one does** — 404, the same body — so hidden cannot be told from
missing (`api.TestATitleOutOfScopeDoesNotExist`, which also fails if a new
route naming a title is not in its table). The importer's item query takes the
scope as a required argument, and `library.TestEveryItemReadChoosesAScope` fails
the build when any function reads `media_item` in SQL without the filter or a
written reason for going without it.

Every account that existed before keeps every library, because that is what it
had. A grant can be no wider than the grantor's own: a restricted approver
grants only roots they can see and a ceiling at or below theirs, at approval
and in an invite (`authz.TestAGrantIsNoWiderThanTheGrantors`). An administrator
changes an existing account with `PUT /api/v1/admin/users/{id}/access`, audited
as `user.grants.changed`, from the account's next request.

A title's rating is the provider's US certification, fetched by
`metadata.ratings` as `system:ratings` (browse only), or a person's, set with
`library.edit` and audited as `media.rating.changed`; the task never replaces a
person's. **Rating a title is deciding who can see it**, which is why it is
audited: a Manager who rates an R film G shows it to every account capped at G.

**Downloading an original is its own permission** (`media.download_original`,
Manager and Admin) rather than browsing, because a download is a copy that has
left the instance where a stream is somebody watching. It is scoped like
playback, rate-limited to 30 an hour, and audited as `media.downloaded`
(ADR-0038).

**Not scoped, on purpose:** the queue and a download's import history
(`acquisition.queue` — a release name already says what it is), the audit log
and notifications. They are operations surfaces with their own permissions.

---

## Administering accounts

**No path leaves the instance without an administrator who can sign in.** Two
independent mechanisms, and the second exists because the first will not last:

1. `authz.CanModifyUser` refuses an account acting on itself, on a peer, or on
   a superior. Admin is the top rank, so today the last administrator cannot be
   suspended or demoted by anyone. This also closes escalation *by denial* — a
   Manager suspending an Admin would neutralise them as effectively as any
   promotion would.
2. `guardLastAdmin` refuses a change that would leave zero administrators able
   to sign in — asked *before* the change, of the account it is about. It is
   consulted by both the role-change and the suspension paths. Today it never
   fires; the moment role editing lands it is the only thing left.

"Able to sign in" is the whole definition: an account that is suspended, or that
never enrolled an authenticator, cannot rescue an instance, and counting one as
an administrator is exactly how somebody ends up locked out with a database row
insisting they are fine.

The check that keeps this honest is a mutation: **disable `CanModifyUser`'s self
and rank rules and `api.TestTheLastAdministratorCannotBeRemoved` must still
pass.** It did not, the first time — `guardLastAdmin` was wired into the role
path and not the suspension path, and a Manager suspended the only administrator
with a 200 OK. Anything added to this surface should be checked the same way.

**Reactivation restores access somebody had; it does not grant access they never
established.** An account that never enrolled an authenticator returns to
`awaiting_mfa`, not to `active`. There is no administrative path that produces
an account able to act without a second factor.

**Suspension is immediate and total**: sessions, API tokens, and any invites the
account issued are all revoked in the same operation. Sessions are server-side
records re-read on every request, so it takes effect on that account's next
request rather than when a token would have expired.

**An instance has exactly one administrator, and that is deliberate.** Every role
assignment goes through `authz.CanAssignRole`, which is strictly downward, so the
setup wizard's Admin is the only one there will ever be. Letting an Admin mint
another was considered and rejected: a peer cannot be modified, so the promotion
would be **irreversible from inside the application** — and a stolen admin
session, which today expires and can be revoked, would become permanent
independent access with its own password and authenticator. The recovery problem
needs bad luck; that persistence problem needs only an attacker.

Getting back in is therefore a host operation, below.

**Roles below Admin can be edited** (ADR-0039), within limits that keep the
one-administrator property: Admin is not editable and is reseeded at every
start; a role may be edited only by an actor who outranks it, with permissions
the actor holds; `admin.system`, `admin.users` and `admin.network` are never
granted below Admin; `auth.login` cannot be removed. Each edit is audited as
`user.role_permissions.changed` and is a *Security* notification.

**An administrator can create an account without knowing its password.** It
starts with a random one nobody holds and a one-time link, valid three days,
that sets one; the person then enrolls an authenticator like anyone else.

**Changing an email needs the current password, from a session.** An API token
cannot, because the address is where a reset would be delivered.

---

## Break-glass recovery

```
cmediastack -config /config/config.yaml -recover <username> [-recover-password]
```

Resets an account's authenticator — and optionally its password — from the host.
It requires a shell on the machine, the data directory and the master key, which
is stronger authentication than anything this application can offer, and it adds
**no reachable surface**: no route, no permission and no principal leads to it.
`identity.TestOnlyTheCommandLineCanRecoverAnAccount` reads the source of every
package and fails the build if it is called from anywhere but the console.

What it does **not** do is the part that matters:

- **It does not return a working session.** The account goes to `awaiting_mfa`
  and must enroll through the normal flow. MFA is mandatory (§13), and a
  recovery path that skipped it would be the way around that requirement rather
  than an exception to it. Verified against the binary: after recovery the
  password alone answers `enroll`, and every authenticated route still refuses
  until a new authenticator exists.
- **It does not grant or change a role.** It restores an administrator; it
  cannot create one.
- **It accepts no password on the command line.** An argument lands in shell
  history and in `ps` output for every user on the host. Stdin only.

It revokes every session and API token the account held, because the reason for
running it may be a compromise rather than a lost phone. The old recovery codes
are destroyed with the enrollment they belonged to, and the TOTP replay counter
is reset.

**It fails if it cannot be audited.** Uniquely in this codebase — everywhere else
an audit write failure is logged and tolerated. An unrecorded credential reset on
an administrator is indistinguishable from an attack. The record's actor is
`console:recovery` with **no user id**, because the actor is whoever had a shell
on the host and the log cannot know who that was. Watch for `user.recovered` in
the audit log: outside a recovery you performed, it is the most serious single
line this system can produce.

---

## Acquisition requests

A request surface is, structurally, a way for people who are not the operator to
cause an acquisition on a machine the operator is answerable for (§13). Three
properties follow.

**Approval acquires nothing.** Approving a request marks it as something this
instance is willing to fetch; a person then searches, sees what came back, and
grabs a release. The machine never chooses what to download. Automatic
fulfilment would mean title-to-release matching deciding acquisitions, and that
matching is the part of this system with the longest list of known failures
(ADR-0016).

**Who sees whose request is a scope, not a permission.** A request is a
statement about what somebody watches. Routes are gated on `request.submit`, and
the listing is narrowed to the caller's own requests unless they hold
`request.approve` — computed from the principal in `request.Service.List`, not
in a handler that could forget. The store holds no permission checks and
documents why: one that silently filtered by principal would make the scoping
invisible at the call site. The follower list — who else is waiting — is shown
only to an approver.

**One account cannot occupy the whole queue.** Twenty outstanding requests per
account, counted inside the insert's transaction so two concurrent submissions
cannot both take the last slot. Answered 429, not 403: the caller is permitted,
they have used their share.

Denials carry a reason, enforced at the API, and the requester can read it.

**Which title satisfies a request is an approver's decision** (ADR-0028). An
approved request is linked to a library item by somebody holding
`request.approve` — never by the requester, and never by matching the request's
words against the library (`request.TestLinkingARequestNeedsThePermissionToApprove`,
`api.TestLinkingARequestIsTheApproversAlone`). Linking adds nothing and acquires
nothing: the item must already exist, added under `library.edit`. Only an
approved request can be linked, only to an item of the kind it asked for, and a
refused link changes nothing (`request.TestOnlyAnApprovedRequestCanBeLinked`,
`request.TestARequestIsLinkedOnlyToAnItemOfItsKind`). Every link is audited as
`request.linked`, with the item it replaced.

A linked request is fulfilled when its item has a file — by the import that put
it there, by the link itself if one is already there, or by the hourly scan if
somebody put one there by hand. The two paths with no person behind them take no
principal, for decision 7's reason: they only move *approved* to *fulfilled*,
for a request a person approved and a person linked, when a file is there
(`request.TestAnImportIntoTheLinkedItemFulfilsTheRequest`,
`request.TestAFileAScanRecordedFulfilsTheLinkedRequest`,
`main.TestAScanThatFindsALinkedTitlesFileFulfilsTheRequest`). The name of the
linked item is shown only to a caller who may browse the library — in the
request, and in the answer to linking it
(`api.TestARequestNamesItsItemOnlyToSomebodyWhoMayBrowse`,
`api.TestALinkNamesTheItemOnlyToAnApproverWhoMayBrowse`).

---

## Automatic acquisition

[ADR-0030](docs/adr/0030-automatic-acquisition.md). With `acquisition.automatic`
on, two scheduled passes fetch what the Wanted screen lists with no person
pressing anything: every fifteen minutes each indexer is asked once for its
recent releases, and a few wanted items are searched for. That is a machine
acquiring content in the operator's name, which §13 makes the operator
answerable for, so what it may do is narrow on purpose.

**It is off** unless the configuration turns it on, and the lint refuses it
with the download engine off. It is not a switch on a screen: it changes what
the instance does with nobody watching, and the file is where that is reviewed.

**It acts on the Wanted list and nothing else.** It never adds a title, and it
replaces a file only when `acquisition.upgrades` says it may (ADR-0036, off by
default): a file below its title's cutoff, by a release the profile says is
better, with the old file moved to the trash. It takes a season pack only for a season that is settled and
every episode of which is wanted, and only once the pack's `.torrent` has been
read and seen to hold every one of them (ADR-0033); a pack offered only as a
magnet link, which says nothing about what is inside, it leaves to a person.
Every candidate goes through the
matching a person's episode or film search runs, and must be accepted by the
title's own quality profile or, for a title without one, the default
(ADR-0035); with no default, nothing is fetched. Choosing a title's profile is
`library.edit`, and audited, because it changes what is downloaded unattended. What a person may
choose and a machine may not — a dead torrent, a release that was ever in the
queue, a second download for an item already under way, a name that fits two
titles in the library, a release named in a language — it refuses, and says why
beside the item on the Wanted screen.

**Its authority is its own.** The passes run as `system:acquire`, minted where
the tasks are registered, holding browse, interactive search and the queue:
no library editing (so it cannot change what is monitored), no deletion, no
root folders, no settings. A pass checks all three permissions itself, so no
other caller can drive one on less.

**It is budgeted.** One recent-release request per indexer per interval; three
searches a pass by default, never-searched first; six hours' back-off after a
search that found nothing, doubling to a week; an hour after one that failed; a
release whose fetch failed left alone for six hours; alternative titles read at
most a hundred a pass and cached for a day; five grabs a pass at most. Neither
pass runs at start, so a crash loop is not a request loop. With egress enforced
and the tunnel not verified, neither asks any indexer anything.

**It is on the record.** Every grab it makes — and every attempt that failed — is
an `acquisition.grabbed` audit line by `system:acquire` with no user id, naming
the release, what it was for, the indexer and how it was found, and never the
download link, which carries the indexer's API key on many trackers. The queue
row says *automatic acquisition*.

**What it grabbed and cannot finish, it gives up** (ADR-0034). A download of
its that makes no verified progress for `download.stall_after` (a day) while the
instance runs is stopped and left in the queue — so that release is never
grabbed again — and its item is looked for afresh; an `acquisition.stalled`
line by `system:download` says so. A person's download is never stopped this
way: it is marked on the Queue screen, and removing it stays their decision.

**What it downloads, it seeds**, exactly as a person's grab does (ADR-0014):
indefinitely unless the indexer records an obligation or `download.seed` is
off. Turning automatic acquisition on is turning on distribution of whatever it
fetches.

**A pack's file list is the uploader's.** The names in a `.torrent` are read
to decide which file is which episode, before the download and again at import,
and are never used as paths: the plan is made from names alone
(`importer.PlanPack`), and at import every file is opened through the
download's contained source exactly as a single download's is (ADR-0015), so a
`../` in a pack is refused by the kernel, not by a string check. Each file of a
pack is then one episode's import — the same upgrade rule, the same trash for
what it supersedes — so a pack cannot replace a better file, and two files
claiming one episode are both refused rather than one chosen.

**Albums are fetched under the same rules** (ADR-0047). A pass of its own,
`acquire.albums`, as `system:acquire`, under the same lock, gate and budget:
it looks only for the Wanted screen's albums whose track list is known, grabs
only a release `search.MatchAlbum` sealed to the album that names its format,
has seeders and was never in the queue, audits every grab the same way, and
grabs an album **once** — an album a download was imported for is left to a
person, so an unreachable bonus track cannot make it fetch forever. Books
(ADR-0050) go through the same runner as `acquire.books`, and only a book with
an author is looked for
(`acquire.TestAWantedAlbumIsFetched`, `acquire.TestAnAlbumIsFetchedOnlyByTheRules`,
`acquire.TestOnlyTheAlbumsOwnAcceptedReleaseIsGrabbed`).

**A book is grabbed and filed the same way** (ADR-0049). Its search issues a
ticket only for a release that is the book, sealed to it as a fifth kind of
target (`api.TestOnlyTheBookCarriesATicket`); the queue reads a book target back
only in its exact shape (`download.TestABookTargetIsStoredAndReadBack`); and the
download goes to the books library, which reads it through `os.Root`, writes
only into the book's own folder through the root's contained vault under a name
made safe from Open Library's title and author, and replaces the book's file
only with a better format, into the trash (`books.TestADownloadedBookIsImported`).
A book's file is served only as a download, under `media.download_original`.

**What it cannot see.** A namesake that is not in the library: with only the
American *The Office* in it, a British `The.Office.S01E02` matches it by name,
as it would in a person's search. A year in the release name, or a scene name
the provider lists (*The Office US*), is what tells them apart.

---

## Notifications

One Discord webhook (ADR-0032). Three things are handled explicitly.

**The link is a credential.** Anyone holding a webhook link can post into the
channel as the instance, so it is treated as the metadata key is: set on a
screen, never in a configuration file; checked with Discord before it is kept,
with a request that posts nothing; stored sealed, bound to its own context; and
never returned — not whole, not masked. Its token is in the URL's path, and
Go's transport errors print the URL, so every error from the transport is
reduced to its cause before anything sees it. Only `https` links to Discord's
own hosts, in the shape Discord hands out, are accepted — a generic webhook
would be an SSRF primitive behind a settings screen, and the `notification`
egress profile deliberately does not refuse private addresses.

**What is sent, much of it strangers wrote.** A username chosen at signup, a
release name, the addresses in a flood line. Discord renders markdown, links
and mentions, so each such value goes inside a code span — where Discord renders
none of them — with backticks replaced and control and bidirectional-override
characters removed; every message also switches mentions off and suppresses
embeds. Nothing in a message links to the instance.

**What leaves for a third party.** What is sent is read from the audit log by a
task holding `admin.audit` and nothing else, and chosen by category. Security,
accounts and operations are on by default; the library and requests, which send
titles — what the library holds, who asked for what — are off until the operator
turns them on. A person's address or user agent is never sent, nor an email
address. A failing task's message is sent as the task wrote it, and can name
hosts and paths on the operator's network.

---

## Metadata and artwork

Three new exposures, each handled explicitly.

**A third party's filenames reach the disk.** A provider returns a poster path,
and `filepath.Join(cacheDir, thatPath)` *cleans* — so `"/../../etc/cron.d/x"`
escapes. This is the shape of the project's one real vulnerability (ADR-0016).
Two rules apply in order: the destination is built only from values this
software chose and validated, with the provider's string used for the URL and
nothing else; and the write goes through `os.Root` regardless. The second is
demonstrated rather than asserted — with both application-level checks disabled,
the kernel refuses every traversal and nothing lands outside the cache. A
provider may supply a **plain filename**: one segment, with a picture's
extension. It cannot supply a path, a host, or a query.

**`library.Cache` has no permission checks, by design.** Caching a poster must
not require the authority to rearrange a library. That makes one mistake
serious — a Cache pointed at a media folder would be a permission-free write
path into it — so a Cache requires a marker file it refuses to remove. A media
folder does not have one.

**MusicBrainz and Open Library are asked without a key** (ADR-0044,
ADR-0048), through the egress guard's `metadata` profile like TMDB. What they
learn: that a CMediaStack instance is asking — each request names the software
and its repository in the User-Agent, as both services ask — and what a person
typed into the search, nothing else; no account, no address book, no library
contents. What they can send back is bounded: an identifier is used only when
it has the provider's shape (`OL…W`, a MusicBrainz UUID), an answer is read to
a size cap, and a name from either becomes a folder name only through the rules
that make a film's folder safe (ADR-0015). One request a second, so a person
cannot make the instance hammer either; a failed Open Library connection is
tried once more, a timeout never (`books.TestOpenLibraryIsAskedPolitely`,
`music.TestMusicBrainzIsAskedPolitely`). Asking either is `library.edit`.

**OpenSubtitles learns what a file is** (ADR-0055, ADR-0056), when a person
asks for a subtitle or the six-hourly sweep looks for a wanted one (at most ten
files a pass): its hash, the title's TMDB ids, the language, and the operator's API
key — through the `subtitle` egress profile. Nothing about who asked. The key
and the optional account password are sealed like every other credential and
re-sealed by `-rotate-key`; the key is sent to the API and never to the host a
download link names. What comes back is written into the library only if it is
UTF-8 SRT text of at most 2 MiB, beside its video through the root's contained
vault, never over an existing file (`subtitles.TestOnlyAnSRTIsTaken`,
`subtitles.TestOpenSubtitlesIsAskedAsItAsks`, `subtitles.TestASubtitleIsFetchedForAFile`).

**The subtitle sweep holds browse and nothing else** (ADR-0056). It runs as
`system:subtitles`; no background task holds `library.edit`. What it may write
is only what a person's fetch may — a new SRT sidecar beside a video, never
over a file — and each is audited as `media.subtitle_fetched` by
`system:subtitles` (`authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant`,
`subtitles.TestTheSweepNeedsBrowse`, `subtitles.TestTheSweepIsBudgetedAndBacksOff`).

**Artwork is served from this application's own origin.** A file that is really
HTML, cached as `.jpg`, is stored cross-site scripting against a logged-in
operator. The type is therefore decided by **sniffing magic bytes**, and
anything unrecognised is refused before it is written. JPEG, PNG and WebP only;
SVG is refused outright, because it is XML and executes script. This is not
theoretical: the live CDN content-negotiates and returns WebP from a URL ending
`.jpg`, so the format on the wire genuinely does not match the URL in
production.

**A half-tunnelled instance is refused at boot.** An indexer search discloses
what somebody here wants; a metadata lookup discloses what this instance
*holds*, and an artwork fetch discloses it again from a second host. Proxying
the first and leaving the others direct does not protect an operator — it gives
them a belief that stops them looking. The configuration lint refuses that
combination. It deliberately does not count a namespace-guarded `direct` as
proxied: that guard covers the download process's network namespace, and the
application process, which makes these requests, does not run in it.

**A grab ticket has exactly one spelling.** `Open` decodes base64 strictly.
Without that, when the sealed length is not a multiple of three, the final
character carries bits nothing reads and 15 other characters decode to identical
bytes — so one ticket had sixteen valid tokens. Not a forgery (the ciphertext is
identical, and the AEAD is correct), but anything keying on the token string
rather than on what it decodes to would treat them as different tickets.

**The provider credential is sealed in the settings table** with the instance
master key, bound to its purpose by the AAD, and **no endpoint returns it in any
form** — not masked and not a prefix, because a masked key still discloses its
shape and is how a guess gets confirmed. A key that does not work is verified
against the provider *before* being written, so it never reaches the database,
and the response says so explicitly.

**An episode list comes from the provider and from nowhere else.** An episode
row is a claim that the episode exists, and the one function that may create
one takes provider data by construction; `library.TestNoEpisodeIsEverCreatedFromAFile`
reads the source and fails the build if any other file writes the season or
episode tables. The refresh runs as its own principal holding **browse and
nothing else**, so it cannot switch a season the operator turned off back on
(`authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant`,
`library.TestARefreshNeverOverridesTheOperatorsChoice`). A rate limit stops it
rather than being retried season by season into the same limit
(`tv.TestARateLimitStopsTheRefreshAndKeepsWhatWasRead`).

**Adding a series names it by the provider's id, and only that** (ADR-0025).
`POST /api/v1/media` takes no title and no year — the item is named by the
provider's answer, and a body that carries a title anyway is refused rather
than ignored (`api.TestARequestCannotNameWhatItIsAdding`), because the label is
what the episode search's matching trusts. An add writes rows and nothing else:
no folder, no file, no download (`follow.TestAddingASeriesCreatesNothingOnDisk`),
so it needs no path authority. It needs `library.edit`, checked at the route, in
the service and again in the store, and no background task holds that
permission (`importer.TestAddingNeedsThePermissionToEditTheLibrary`). Its cost
to the provider is bounded — one request for the series and one per season —
and a duplicate is refused before any request is made
(`follow.TestAddingTheSameSeriesTwiceAsksTheProviderNothing`). Each add is
audited as `media.added` with the person, the series, where it lives and the
monitoring chosen.

**A film is added the same way, and what is grabbed for it is filed under it**
(ADR-0026). Adding one is the same route, the same permission, the same rows and
nothing on disk (`follow.TestAddingAFilmRecordsItAsChosenAndWantsIt`); a field a
film does not have — a monitoring choice — is refused rather than ignored. Its
search issues a grab ticket only for a release that **is** the film, and the film
is sealed into the ticket, as an episode is: the client cannot attach a release
to a film the server did not match it to, or to another film
(`api.TestOnlyTheFilmCarriesATicket`, `search.TestAMixedTargetIsNotOpened`). The
queue row records the kind of target it carries, and a row that fits neither
shape is read as having none rather than as half of one
(`download.TestARowThatFitsNeitherShapeHasNoTarget`). The import refuses a film
target whose item is gone, is a series, or whose download is television
(`importer.TestAFilmTargetThatDoesNotFitIsRefused`).

**An album is sealed the same way** (ADR-0046). Its search, under
`acquisition.search` and scoped — a hidden album answers as a missing one —
issues a ticket only for a release that is the album, whatever the search
service returns (`api.TestOnlyTheAlbumCarriesATicket`,
`api.TestATitleOutOfScopeDoesNotExist`); the artist and the album ride in the
ticket, and the queue reads an album target back only in its exact shape
(`download.TestAnAlbumTargetIsStoredAndReadBack`). Its download goes to the
music library's import, which files each track through the root folder's
contained vault and reads the download through `os.Root`, never to the video
importer (`main.TestADownloadForAnAlbumGoesToTheMusicLibrary`).

**An import reads what a finished download left on disk when the engine has let
it go** (found verifying ADR-0026). The directory is the one the engine's own data
directory and the validated hash name — never the torrent's declared name — and
it is walked through `os.Root`, listing regular files only: a symbolic link in a
download is neither followed nor returned, a hash that is not one reads nothing,
and a directory with more files than any release is refused
(`download.TestFilesOnDiskListsWhatTheDownloadLeftAndNothingElse`,
`download.TestFilesOnDiskRefusesWhatIsNotADownload`). What it lists still goes
through the importer's contained source before anything is linked.

---

## Sessions

Sessions are opaque and server-side, not JWTs. §7.2 requires suspension to
revoke sessions *immediately*, and a self-contained token cannot do that
without consulting a revocation list on every request — which is the database
lookup a JWT exists to avoid. So the lookup happens, and the signing-key
management does not.

- The cookie is `<sessionID>.<secret>`; only `sha256(secret)` is stored, so a
  copy of the database does not yield usable cookies.
- The secret rotates every 15 minutes. The previous secret keeps working for 30
  seconds so in-flight requests do not fail.
- Presenting the previous secret **after** that grace window is not a race, it
  is a replay of a superseded token. The whole session is revoked and the event
  is logged at WARN. This cuts off the legitimate holder too; that is the
  intended trade.
- Idle and absolute timeouts are both enforced, and a suspended or disabled
  account's sessions stop working on the very next request.
- The cookie is HttpOnly (so an XSS past the CSP still cannot read it), Secure
  unless serving plain HTTP to localhost, and SameSite=Lax.

---

## Feed tokens

A calendar app and a feed reader send an address and nothing else, so the
calendar and the feed are anonymous routes whose path carries the credential
(ADR-0041): a `cms_feed_` token, one per account, stored as its SHA-256, minted
and revoked from a session only. It reads those two routes and nothing else —
its principal holds `media.browse` alone, in its account's scope — and a wrong,
revoked or suspended account's token answers exactly as a missing route does.
The addresses are masked in every log line and rate-limited. Anyone who holds an
address can read that account's calendar and arrivals until it is replaced:
that is what a calendar subscription is.

## API tokens

Tokens are scoped, revocable, non-interactive credentials. `cms_pat_` prefixed
so that gitleaks and GitHub secret scanning can match one pasted into a public
repository; 256 bits from `crypto/rand`; only the SHA-256 is stored.

Three properties do the security work:

1. **Scope ≤ issuer, checked at issuance.** A partially over-scoped request is
   refused whole rather than quietly trimmed, so the attempt is visible.
2. **Scope re-intersected on every use** against the owner's *current* role. A
   demotion narrows every token that user holds, with nobody having to remember
   to go and revoke them.
3. **Credential management is session-only.** `Route.SessionOnly` refuses a
   token before any permission check on password change, MFA enrollment,
   recovery codes, token issuance and session management. A token lives in a
   script or a config file; if one could replace the password or the second
   factor, a single leak would be a permanent account takeover rather than a
   scoped grant you can revoke.

**CSRF is deliberately not required for Bearer requests.** CSRF exists because
cookies are ambient — a browser attaches them to cross-site requests the user
never intended. An Authorization header is not ambient and cannot be set
cross-origin without a CORS preflight this server never approves. Requiring a
double-submit token there would make tokens unusable for every POST while
defending against nothing. Cookie-authenticated requests still require it, and
`api.TestStateChangingRequestWithoutCSRFTokenIsRefused` holds.

---

## Metrics and scheduled tasks

`/metrics` is served on the **management listener only**, never the public one:
metric names and label values leak operational shape — route names, indexer
names, user and session counts — and none of that belongs on an internet-facing
interface. `api.TestEveryNonAllowlistedRouteRejectsAnonymous` covers the public
router; the management listener is bound separately and the config lint refuses
to boot if it is bound to all interfaces or shares the public address.

HTTP series are labelled by route **pattern**, never raw path, so no caller can
create unbounded time series by varying an ID. Label values are escaped, so a
hostile string reaching a label cannot inject a fabricated metric line.

The exposition format is implemented in-tree rather than via
`prometheus/client_golang`. See [ADR-0010](docs/adr/0010-metrics-in-tree.md) —
the decisive reason is that this build environment cannot reach
`sum.golang.org`, so that dependency's checksums could not be verified against
the transparency log, and shipping an unverifiable `go.sum` would make the
claim below false.

---

## Static analysis

Four CI jobs read the code rather than run it — lint, SAST, the dependency
audit and the secret scan — and **none of them fails on this tree**. The first
two report nothing at all: golangci-lint (errcheck, errorlint, gosec, nilerr, noctx,
rowserrcheck, sqlclosecheck, staticcheck and the rest in `.golangci.yml`) and
gosec on its own, as the SAST job runs it — severity and confidence medium,
failing the build on any finding. Both are **pinned** — golangci-lint v2.14.0,
gosec v2.29.0 — so a new rule arrives in a change that deals with what it
finds, rather than turning the build red on its own.

Configuration silences two things only: errcheck and gosec in test files, as
before, and two words misspell does not know. Every other exception is on the
line it excuses, with the reason beside it:

| Where | Rule | Why it stays |
|---|---|---|
| `identity/totp.go` — `crypto/sha1` | G505 | HMAC-SHA1 is RFC 6238's default and what authenticator apps implement. HMAC does not rest on SHA-1's collision resistance |
| `identity/breach.go` — `crypto/sha1` and the one sum | G505, G401 | Pwned Passwords' range protocol is defined over SHA-1: the digest is the corpus's index, its first five characters are all that leave, and it is never stored or used as a password hash (ADR-0051) |
| `identity/breach.go` — the service's address | G101 | `https://api.pwnedpasswords.com` is a public address; the rule reads *Passwords* in its name |
| `identity/feed.go` — the feed token's prefix | G101 | `cms_feed_` marks a token's kind so logs can mask it; it is not itself a credential (ADR-0041) |
| `subtitles/match.go` — the file's size in the hash | G115 | A file's size, checked to be at least 128 KiB on the line above, converted to the unsigned sum OpenSubtitles' hash is defined over (ADR-0055) |
| `api/sessionauth.go` — the session cookie, set and cleared | G124 | `HttpOnly` and `SameSite` are literal; `Secure` comes from the configuration, true for every deployment but plain HTTP to `http://localhost`, where it would make signing in impossible |
| `indexer/cardigann.go` — a tracker cookie the operator pasted | G124 | A cookie this instance *sends* to the tracker as a client, never one it serves: `Secure`, `HttpOnly` and `SameSite` are a server's instructions to a browser (ADR-0059) |
| `api/auth.go` — the CSRF cookie | G124 | Readable by script by design: that is the double-submit pattern, and the value is worthless without the session cookie, which is `HttpOnly` |
| `playback/sandbox.go` — starting `ffprobe` and `ffmpeg` | G204 | The tool is checked against the two binaries the sandbox resolved at startup; the arguments are built there and name the file as `/dev/fd/3`; no shell is involved |
| `platform/config/config.go` — reading the configuration | G304 | The path is the operator's own `--config` |
| `platform/backup` — seven file opens | G304 | Five open names the package built from a timestamp and a constant prefix, in the configured backup directory or beside the database; two open the operator's own `-verify-backup` / `-restore-backup` and `-restore-to` arguments |
| `platform/db/db.go` — creating the database file | G304 | The operator's configured `database.path`, created `0600` before SQLite opens it |
| `acquire/store.go` — recording what a search found | G202 | The one concatenated name is a column — `episode_id`, `item_id` or `album_id` — chosen from those constants just above; every value is bound |
| `docs/cmd/gendocs` — writing the generated documents | G306 | Documents committed to the repository, readable like every other file in it |
| `platform/audit/audit.go` — four action names | G101 | `auth.api_token.issued`, `.revoked` and the feed token's two are names of events, not credentials |
| `api/sessionauth.go` — a token or cookie that does not resolve | nilerr | It is no credential: the request is anonymous, which every protected route refuses. It never falls back from a bad token to the cookie |
| `identity/service.go` — a duplicate address at signup | nilerr | Answered exactly like a new one, so the form cannot test whether an address is registered. Only a uniqueness violation is masked; any other failure is reported (`api.TestASignupThatCouldNotBeRecordedIsNotReportedAsRecorded`) |
| `identity/reset.go` — a reset for an unknown account | nilerr | The same answer for every username, by design; the audit line says which it was |

The rest are not about security and are explained where they stand: an
import record whose time cannot be read is answered "retry"; a season pack's
file that is skipped is an outcome, not a failure; sidecar subtitles that cannot
be listed cost only themselves; a music or books scan goes on past a directory it
cannot read; an album is shown while its track list cannot be fetched; and one
test compares an error's identity on purpose.

**The dependency audit** (`govulncheck`, reporting only what the call graph
reaches) found three vulnerabilities in modules the torrent library pulls in —
`gorilla/websocket` (a weak random source for WebSocket masks), `pion/dtls` and
`pion/stun` (each a panic on a crafted message) — and one, in OpenTelemetry,
imported but not reached. All four modules are now at fixed releases; the
audit reports none reached and none imported. What remains is
`golang.org/x/crypto/openpgp`, which has no fix and which nothing here imports.
The new `go.sum` lines were checked against the checksum database, and
`go mod verify` passes.

**The secret scan** (gitleaks) flagged seven values, every one a made-up
credential a test needs: `invited-passphrase-1`, `victim-passphrase-9`, a
documentation passkey. `.gitleaks.toml` allowlists those values — not test
files, so a real secret committed to a test is still found, which was checked by
planting one. The scan was run on the working tree; the repository's history,
which the weekly run reads, was not available here, and an allowlist by value
covers the same strings wherever they appear in it.

---

## Not yet enforced

Stated plainly so nobody assumes otherwise. Tracked in PROGRESS.md.

- **There is no mail transport,** so `POST /api/v1/auth/reset/initiate` creates
  a valid token that nothing delivers. The response is deliberately identical
  either way — admitting delivery failed would confirm the account exists. The
  working path is `POST /api/v1/admin/users/{id}/reset-link`, which returns a
  single-use token for an administrator to pass on out of band.
- **Signup does not validate an email address or a username** beyond uniqueness. An administrator's new account and an email change do (ADR-0039).
- **Per-library visibility and rating ceilings were not enforced until 4ac**
  (ADR-0037, *Authorization model* above). What they still do not cover: a
  restricted account with `library.edit` that adds a title already held outside
  its scope is told it is already in the library; the queue names every
  download; a series has one rating, not one per episode; and a title the
  provider has not yet rated is hidden from a capped account for up to an hour.
- **Nothing notifies a person individually.** Notifications go to one Discord
  channel, the operator's (ADR-0032); a requester learns their request was
  fulfilled by looking, or from that channel if the operator shares it and turns
  *Requests* on. There is no mail transport.
- **A request does not wait on its own.** "Keep looking until it appears" is
  now automatic acquisition's (ADR-0030) — for what is in the library. A request
  is looked for once an approver adds its title, and only with automatic
  acquisition on; until then it waits for a person.
- **No second administrator can be created** — deliberate; see *Administering
  accounts* and *Break-glass recovery*.
- **No account deletion.** Suspension is the reversible answer; deletion needs
  its own decision about what becomes of that account's audit history, and an
  audit trail that can be erased by deleting its subject is not one.
- **No per-user permission overrides.** Roles only.
- **The audit log is never trimmed, and has no export.** It grows with what
  people and tasks do — a few megabytes a year once anonymous denials have a
  ceiling. The paginated API (`GET /api/v1/admin/audit`, with a token scoped to
  `admin.audit`) is the export for a script, and backups carry the whole log;
  trimming it would be a decision with its own record (ADR-0031).
- **Over-ceiling denials of an hour not yet over are lost if the process dies.**
  They are counted in memory; a stop the process is told about writes them first.
- **The breach check fails open.** When Have I Been Pwned cannot be reached, a
  password is accepted on the policy's own rules and a warning is logged
  (ADR-0051): an outage elsewhere must not stop people setting passwords.
  Break-glass recovery at the host never checks.
- **Signup's proof-of-work is spent in memory.** A restart forgets which
  challenges were used; each lives ten minutes, which is the most a replay
  across a restart could win (ADR-0051).
- **Unprivileged user namespaces are permitted inside the container.** This is
  the residual of closing the seccomp gap, and it is a real trade rather than a
  free win. `deploy/seccomp-cmediastack.json` is Docker's default profile plus
  one allowance for `clone` with `CLONE_NEWUSER|CLONE_NEWPID|CLONE_NEWNET`,
  because the media-parser jail (ADR-0020) cannot exist without it — the stock
  profile permits `clone` only when *no* namespace flag is set.

  What that gives back: user namespaces have a long history of local
  privilege-escalation CVEs, and a process that can create one reaches kernel
  code that would otherwise be unreachable from this container. What it buys:
  ffprobe and ffmpeg — the largest hostile-input surface in the system — run
  with **no network at all**, so a parser exploit cannot call out, cannot reach
  the LAN, and cannot reach the NFS server the media is mounted from.

  The alternative was the stock profile *and* an unsandboxed parser, which is
  the same kernel surface reachable by a **compromised ffmpeg** rather than only
  by the application. The allowance is narrow: `CLONE_NEWNS`, `CLONE_NEWUTS`,
  `CLONE_NEWIPC` and `CLONE_NEWCGROUP` all remain denied, as do `setns`,
  `unshare` and `clone3`, so this is one shape of namespace and not the
  capability in general.

  **Verified in a container, not only on paper.** Under Docker's stock profile
  the boot log reads *"media parser sandbox is NOT available: this kernel
  refused to create a user namespace"*; with this profile it reads *"media
  parser sandbox is available"* and a real probe reports `"sandboxed": true`.
  Both halves of the claim were run rather than derived.
- **The ffmpeg and ffprobe binaries in the image come from a third party.**
  `mwader/static-ffmpeg`, pinned by digest so a rebuild cannot silently get
  different bytes, copied into the distroless runtime at mode `0555` — readable
  and executable by all, writable by none, so the process cannot rewrite the
  parser it executes.

  This is a supply-chain dependency in the trust path of the component the jail
  above exists to contain, and it is stated rather than buried. The
  alternatives were `apt-get install ffmpeg`, which puts a package manager and a
  shell back into an image whose whole purpose is to have neither, and building
  ffmpeg from source in the Dockerfile, which is a maintenance project of its
  own and whose likely failure mode is a worse ffmpeg than the pinned one.
- **Restoring does not put a file back at its original path.** The original is
  not recorded — deliberately: a stored path would mean a restore could write
  anywhere that record named, while a name derived now is one the current
  containment rules apply to.
- **Nothing warns before deleting an item whose bytes are shared with a
  still-seeding download**, though the file listing already reports
  `hardlinked`.
- **Upgrade decisions use the DEFAULT quality ladder**, not the profile that
  grabbed the release — the grab does not record its profile on the queue row
  yet. An operator whose profile prefers 1080p WEB-DL to a 2160p remux is not
  consulted. Both files survive, so the cost is a skipped upgrade, not a lost
  file. Documented at the seam in `release.DefaultRank`.
- **Without a metadata provider**, identification is from the release name
  alone: no external ids, no posters, and no episode list — deliberately, since
  inventing one from the files held would make every season 100% complete,
  which is worse than no answer. With a provider configured all three exist
  ([ADR-0018](docs/adr/0018-metadata-and-artwork.md),
  [ADR-0022](docs/adr/0022-episode-tracking.md)). This entry used to say
  there was no provider at all, and went on saying it from increment 3i, which
  built one, until 4k.
- **A season pack is refused, not split.**
- **A scan does not notice a file that MOVED.** It reads as one file missing and
  one added, which is correct but loses the file's history — its info hash, and
  which download produced it.
- **Files found by a scan are recorded as not hardlinked**, because this
  software did not place them and cannot know whether something else shares the
  inode. That is the honest answer and the safe one: an operator deleting a file
  is warned about sharing only where it is known.
- **`Vault.Link` is the one operation not purely kernel-contained**, because the
  source is the completed download, outside the library by design. It creates
  the parent through the root, links, then verifies the result back through the
  root and removes it if the inode is wrong. That **detects** rather than
  prevents a swap race, and reaching that race requires write access to the
  library already. Documented on the method, not left to be discovered.
- **Seeding is distribution.** It is the part of this software that sends
  content to strangers, and §13 puts its legality on the operator. When an
  indexer records no obligation the instance seeds **indefinitely** — see
  ADR-0014 for why that is the right default and why `download.seed: false` is
  the switch that stops it.
- **The queue holds `.torrent` blobs in SQLite**, capped at 2 MiB each by the
  fetch path. Unremarkable at this scale; worth revisiting at tens of thousands
  of rows.
- **Only v1 (SHA-1) info hashes are accepted.** A v2 hash is refused by name
  rather than producing a confusing "no such transfer".
- **This code verifies the network jail; it does not build it.** ADR-0001's
  nftables rules and the WireGuard container are the operator's to deploy, and
  they are what the §2 guarantee actually rests on.
- **The leak test cannot prove the absence of a leak** — a leak takes a path
  this process cannot observe. It proves routing matches the configuration, and
  its own response body says so.
- SSRF address filtering is in the egress guard (`DenyPrivate`) and applies to
  every profile that dials an address derived from hostile input. It has
  exactly one exemption (ADR-0024, approved by the operator): **an indexer's
  own configured host and port** may be on the operator's network, so a
  Prowlarr or Jackett on the LAN works. Nothing a feed names shares it, and
  link-local stays refused even there. (This line used to say path
  containment and XML hardening were still to come; path containment is
  `os.Root` since 3a, ADR-0015, and feeds are parsed with `encoding/xml`,
  which expands no entities a feed declares.)
- The `downloader` role refuses to start rather than running without its
  network jail. This is implemented and verified, not aspirational.
- **The UI requires JavaScript** (see below). Enrollment itself is complete:
  the page shows a scannable code, a phone tap-through, and manual key entry.
- **The UI requires JavaScript.** There is no no-JS fallback, deliberately: a
  parallel set of server-side form handlers would be a second authorization
  path, and the one you forget to check is the one nobody tests.
- Admin-minted password-reset links are API-only. (Suspension, reactivation and
  role changes have had a screen since 3f; this line used to say none did.)

---

## Operator responsibilities

1. **Set `CMS_TRUSTED_PROXIES` only if a reverse proxy actually fronts the
   app**, and only to that proxy's address. Trusting `X-Forwarded-For` from
   anywhere else makes every per-IP rate limit spoofable by setting a header.
2. **Keep the management listener off the public interface.** The config lint
   refuses to boot if it is bound to all interfaces or shares the public
   listener's address, but it cannot see your firewall.
3. **Put an authenticating proxy in front.** With open registration on a public
   listener, the signup route is the most-attacked endpoint and it performs
   Argon2id hashing. An authenticating proxy means an internet scanner never
   reaches application code. Strongly recommended, not assumed.
4. **Back up the master key separately from the database** — and keep a copy
   off the host: the backups are unreadable without it.
5. **Finish the first-run wizard before the instance is reachable** (see *The
   anonymous surface*).
6. **Keep backups off the database's disk** — `backup.dir` on another disk or the
   NAS, or copied there. The Backups screen says when they share the database's
   filesystem.
7. **Look at the Audit log screen now and then.** Its first line counts the last
   week by kind. A run of `authz.denied` from one source, or any
   `authz.denied.suppressed` line, is either an attack or a bug, and both are
   worth knowing about.
8. **Set up notifications** (ADR-0032) — a Discord webhook on the Notifications
   screen — so that a failing backup, a tunnel down or somebody with your
   password but not your authenticator reaches you rather than waiting to be
   found. Turn *Library* and *Requests* on only knowing that they send titles to
   Discord.
9. **Turn automatic acquisition on only when the Wanted list is what you want
   downloaded** — and the tunnel, the indexers and a default profile are set up.
   It acts in your name: each grab is audited as `system:acquire`, and what it
   downloads, it seeds (see *Automatic acquisition*).
