# ADR-0039: Account administration — roles edited, accounts created, a profile changed

**Status:** accepted
**Date:** 2026-09-29
**Related:** [ADR-0017](0017-acquisition-requests.md),
[ADR-0037](0037-libraries-and-rating-ceilings.md), SECURITY.md (*Administering
accounts*, *Break-glass recovery*)

## The problem

Five account routes registered in Phase 1 still answer 501:

- a pending account request read on its own (`GET /api/v1/accounts/requests/{id}`)
- an administrator creating an account (`POST /api/v1/admin/users`)
- editing one's own profile (`PATCH /api/v1/me`)
- the roles and their permissions (`GET /api/v1/admin/roles`)
- editing a role's permissions (`PATCH /api/v1/admin/roles/{id}`)

The three built-in roles are fixed: the seed rewrites their permissions at every
start, so an operator who wants a User who may download originals, or a Manager
who may manage indexers, cannot have one.

## Decisions

### 1. A pending request, on its own

`account.approve`, the permission that lists them: the request as the list shows
it. A request that was decided, expired or never existed is 404, all alike.

### 2. Roles are listed, and a role below Admin can be edited

`GET /api/v1/admin/roles` lists every role: its rank, its permissions, whether
they are the built-in defaults, and how many accounts hold it. `admin.users`.

`PATCH /api/v1/admin/roles/{id}` with `{"permissions": [...]}` replaces a role's
permissions, and `{"defaults": true}` puts back the built-in ones. The rules:

- **Admin is not editable.** It holds every permission, always. An Admin role
  that could lose `admin.users` is a way to lock the instance out of its own
  administration.
- **Only a role the actor outranks**, and only permissions the actor holds. It
  is the rule that governs assigning a role, applied to changing one.
- **Three permissions stay Admin's:** `admin.system`, `admin.users` and
  `admin.network`. Each is instance-wide trust. `admin.system` also means every
  library (ADR-0037); `admin.users` administers accounts; `admin.network` is the
  egress guarantee. Granting any of them to a lower rank would make a second
  administrator in all but name, which the risk register already rejected.
  Every other permission may be granted.
- **`auth.login` cannot be removed.** A role without it is every holder
  suspended at once, without the audit trail a suspension leaves.
- Rank and name are not editable.

An edited role is **kept across restarts**: migration 0023 records when its
permissions were last chosen, and the seed leaves a chosen role alone. Admin is
still rewritten at every start. Every holder's next request is judged by the new
set, and so is every API token, since a token's permissions are its own
intersected with its role's. Audited as `user.role_permissions.changed` with before
and after, a *Security* notification.

### 3. An administrator creates an account

`POST /api/v1/admin/users` with a username, an email, a role and the libraries
and rating ceiling (ADR-0037). `admin.users`, the role strictly below the actor,
and a grant no wider than the actor's.

**The administrator never knows the password.** The account is created in
`awaiting_mfa` with a random password nobody holds, and the answer carries a
one-time link to set one, valid for three days: the reset path, used to start
an account rather than recover it. The person sets a password, signs in and
enrolls an authenticator, exactly as after an invite. Audited as `user.created`.

### 4. Changing one's own email

`PATCH /api/v1/me` with `{"email": ..., "current_password": ...}`. The email is
the one thing on a profile worth changing: the username is how the audit log
names the account, and changing it would sever the account from its own history.
**The current password is required**, as for a new password, and the route is
**session-only**: an API token is a credential that lives in a script, and it
must not be able to redirect where a reset would be delivered. Audited as
`user.updated`. A taken address is refused by name.

## Rejected alternatives

**New roles.** A household instance has three kinds of person. Custom roles add
rank arithmetic nobody will reason about, and editing the three covers what they
are for.

**An administrator choosing the new account's password.** Somebody would then
know a password they did not choose, and it would travel over chat. A link that
sets one, used once, does not have that problem.

**Username changes.** Every audit line names the account by its username.

## Known limitations

- Nothing mails the link or the changed address: there is no mail transport.
- A role's edit applies from the next request, not to a request in flight.

## Verification

| Claim | Test |
|---|---|
| A pending request reads alone; a decided one is 404 | `api.TestAPendingRequestIsReadAlone` |
| Roles are listed with their holders and whether they are the defaults | `api.TestRolesAreListed` |
| Admin is not editable; admin.system, admin.users, admin.network and removing auth.login are refused; the edit applies at once and is audited | `api.TestARoleIsEditedWithinLimits` |
| An edited role survives a restart; defaults put back the seed | `identity.TestAnEditedRoleSurvivesTheSeed` |
| An account created by an administrator starts with a link, not a password; the grant is held to the actor's | `api.TestAnAdministratorCreatesAnAccount` |
| One's email changes with the current password, from a session only | `api.TestAnEmailIsChangedWithThePassword` |
