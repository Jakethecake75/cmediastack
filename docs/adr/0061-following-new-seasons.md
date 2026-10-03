# ADR-0061: Whether a series' new seasons are followed

**Status:** accepted
**Date:** 2026-10-03
**Related:** [ADR-0022](0022-episode-tracking.md)

## The problem

A season seen for the first time starts monitored, unless every regular
season of the series is off (ADR-0022): that is how "I have stopped
following this" is said. Nothing says the other thing Sonarr's *monitor new
items* switch says: keep the seasons I have chosen, but do not take on new
ones. A person finishing an old show they like, who does not want its revival,
has to notice the new season and switch it off before it reaches the wanted
list.

## Decisions

### 1. One switch on the series, on by default

A series has *follow new seasons*, on unless switched off
(`media_item.follow_new_seasons`, migration 0034). When it is off, a season the
provider lists for the first time starts unmonitored, with its episodes.
Seasons already known keep whatever they are set to: switching it off changes
nothing already on the list.

The existing rule stays as it is. A series with every regular season off
still takes no new season, whatever the switch says. The switch adds a way to
say no; it never makes a new season monitored where today's rule would not.

### 2. Set like a season's monitoring

`PUT /api/v1/media/{id}/new-seasons` with `{"follow": false}` needs
`library.edit` and is scoped like every title route: a series out of the
caller's libraries does not exist. A film, an artist or a book is not a
series and answers as one that does not exist. The series' seasons
(`GET /api/v1/media/{id}/children`) say whether it is on, and the series page
has the switch.
