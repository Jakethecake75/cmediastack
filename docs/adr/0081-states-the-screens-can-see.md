# ADR-0081: States the screens can see

**Status:** accepted
**Date:** 2026-11-25
**Related:** [ADR-0069](0069-home-request-settings.md) (sections and tabs), [ADR-0078](0078-a-title-has-a-page-and-home-shows-pictures.md) (a title's page), [ADR-0040](0040-operations-routes.md) (health and logs), [ADR-0079](0079-a-samsung-television-app.md) (the remote); the design passes are recorded in `DESIGN_LOOP.md`

## The problem

Five design passes over the web interface changed styles only. They left five
things that a stylesheet cannot reach, because the script or the markup does
not say what the screen needs to show:

1. **A title's page marked no tab.** It is not a tab itself, so the bar under
   the sections showed Home and Library both unmarked and the person lost
   their place.
2. **Health's state was text.** "Overall: warn" and a warn or error log line
   looked exactly like an ok one; a failing check wore the warn badge.
3. **The theme button was announced backwards.** It reads "Light mode" (the
   action) while carrying `aria-pressed="true"` in dark mode, so a screen
   reader said "Light mode, pressed" while the page was dark.
4. **Search had no heading,** unlike Library, Downloads, Wanted and every
   settings screen.
5. **Every absent episode looked the same, and none could be reached.**
   Missing, still to air and announced shared one grey card, though only
   missing is a problem. They were `disabled` buttons, which a keyboard skips
   and the television remote (`remote.js` skips disabled elements) cannot
   land on, so a remote user never learnt that an episode was missing.

## Decisions

1. **A view that is not a tab keeps the tab it was opened from marked.** The
   app remembers the last tab open in each section; a title's page or the
   player marks that one, or the section's first tab when the page was opened
   directly by its address.
2. **The state is a class as well as text.** `#health-summary` gets
   `state-warn` or `state-fail`; each log line gets `level-<level>`; a
   failing check gets the `bad` badge. The words are unchanged.
3. **The theme button is an action, not a toggle.** Its label already says
   what pressing it does; `aria-pressed` is removed from the markup and the
   script.
4. **Search has an h2, "Search".**
5. **An absent episode says which kind it is and stays reachable.** The card
   gets `missing`, `upcoming` or `announced` beside `absent`. It is no longer
   `disabled` but `aria-disabled="true"`, and its click does nothing unless
   the episode is on disk, so a keyboard or remote can focus it and hear what
   it is, and still cannot start a file that does not exist. Missing is drawn
   with the warning colour; the others stay neutral.

## Consequences

- No route, API, ID, class or field name changes; classes are only added.
- `scripts/screenshot.js` checks each decision in a browser on every run: a
  title opened from Library keeps Library marked, the theme button has no
  `aria-pressed`, a missing episode is focusable and does not play, and log
  lines carry their level.
