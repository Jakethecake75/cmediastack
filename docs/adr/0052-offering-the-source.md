# ADR-0052: Offering the source, as the AGPL asks

**Status:** accepted
**Date:** 2026-10-01
**Related:** [ADR-0008](0008-licensing.md), [ADR-0011](0011-server-rendered-shells-no-build-step.md)

## The problem

The software is AGPL-3.0 (ADR-0008). Section 13 asks that every user who
interacts with a modified version over a network be prominently offered the
Corresponding Source. ADR-0008 named this "the cheapest AGPL obligation there
is to satisfy" and left it unbuilt. Every page an instance serves is that
network interaction.

## Decisions

### 1. Every page says where the source is

Every page the instance renders carries a footer: *CMediaStack `<version>` ·
Source code (AGPL-3.0)*. That includes the login, signup, reset and setup
pages, which a visitor sees before signing in. The link goes to
`server.source_url`. The pages are rendered from one layout, so no page can
leave it out. Nothing is added to the JSON API: an API client is not a person
being offered anything, and the source is a link away.

### 2. The address is the operator's to set

`server.source_url` defaults to this project's repository,
`https://github.com/jakethecake75/cmediastack`. That is the right answer for an
unmodified build. An operator who runs a modified build owes their own
modified source, and the configuration's comment says so: the default is
correct only for the code it names. The setting must be an absolute `https` or
`http` address. The configuration lint refuses anything else, because a
`javascript:` URL in a footer link is a script.

### 3. The version says which source

The footer shows the build's version, the same string `-version` prints and
the health report carries, so "the source" means the source of *this* build.
