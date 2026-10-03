# ADR-0051: The breached-password check and signup proof-of-work, which the configuration promised

**Status:** accepted
**Date:** 2026-09-30
**Related:** [ADR-0001](0001-egress-control-wireguard-netns.md), [ADR-0011](0011-server-rendered-shells-no-build-step.md)

## The problem

The configuration has two switches that do nothing:

- `auth.breach_check_enabled` defaults to **true**, and its comment promises
  an HIBP k-anonymity range check. No code reads it.
- `registration.proof_of_work_bits` defaults to **18**, and its comment
  promises a hashcash-style proof before the server spends Argon2id time on an
  anonymous signup. No code reads it either.

A switch that reads as on and does nothing is worse than a missing feature.
An operator who reads it believes the instance refuses known-breached
passwords and makes anonymous signups pay for their hashing. Neither is true.
This record builds both as the comments describe them.

## Decisions

### 1. The breached-password check: HIBP's range API, five characters out

When the switch is on, a password a person sets is refused if it appears in
Have I Been Pwned's Pwned Passwords corpus. That covers the setup wizard,
signup, a reset and a change. The check is k-anonymous:

- the password's SHA-1 is computed locally, and only its **first five hex
  characters** are sent, to `https://api.pwnedpasswords.com/range/<prefix>`;
- the answer, every suffix sharing that prefix, is compared locally;
- `Add-Padding: true` is sent, so the answer's size does not narrow down which
  prefix it was.

It goes through the egress guard's `metadata` profile, as the comment
promised, with a 5-second timeout.

**If HIBP cannot be asked, the password is accepted** and a warning is
logged. An outage at a third party must not stop people setting passwords.
The check raises the floor; it is not the floor. That floor is the length
and simplicity rules, which always apply.

**Break-glass recovery at the host (`-recover`) does not check.** It runs
where the operator is, possibly with no network, and it is the way back in
when everything else has failed.

A breached password is refused with a sentence that says so: *it has appeared
in a known data breach; choose another.*

### 2. Signup proof-of-work: a sealed, single-use hashcash challenge

When `proof_of_work_bits` is above zero, a signup must carry a solved
challenge, and the server checks it **before** spending any Argon2id time:

- `GET /api/v1/auth/signup/challenge` (anonymous; 404 when registration is
  closed, as signup is) returns a **challenge** and the number of bits. The challenge is
  a token sealed with the instance's master key, like a grab ticket. It holds
  a random nonce and an expiry ten minutes out, so the server keeps nothing
  until it is used.
- The client finds a counter for which `SHA-256(challenge + ":" + counter)`
  begins with that many **zero bits**, and sends both with the signup.
- The server opens the seal (forged or expired: refused), recomputes one hash,
  and refuses a challenge it has already accepted. Used challenges are held
  in memory until they expire. A restart forgets them, and their ten minutes
  are the most a replay could win.

At 18 bits a browser does about 260,000 hashes, around a second. One signup
then costs the client far more than it costs the server. The page solves it
with a SHA-256 written into `auth.js`, because no third-party code is loaded
(ADR-0011). The signup route's per-address limit and outstanding-request cap
still apply. The proof makes a flood expensive; they bound it.

## Consequences

- One more anonymous route; the anonymous surface is 15.
- `breach_check_enabled: true` now costs one HTTPS request per password set.
  An operator who does not want any request to leave can turn it off.
