# Canary ground truth

Start the app on port 8099:

```sh
uv run uvicorn app:app --app-dir fixtures/canary --port 8099
```

Then, from a terminal (so afford can prompt for `admin` / `secret`):

```sh
AFFORD_USERNAME=admin AFFORD_PASSWORD=secret \
  uv run afford run --config fixtures/canary/afford.toml
```

## Defects planted, and the code each must produce

| route | planted defect | expected code |
|---|---|---|
| `/` | `<button>Refresh</button>` with no handler | `E_DEAD_CONTROL` |
| `/` | `<a href="#">` | `E_EMPTY_HREF` |
| `/` | `target="_blank"` links | `E_NEW_TAB` |
| `/` | popup renders `/overflow` | `E_OVERFLOW_X` with `detail.popup_url` and a popup screenshot |
| `/upload` | file input whose change handler throws | `E_CLIENT_ERROR_ON_ACTION` (proves the file was attached) |
| `/crash` | async throw after paint | `E_UNCAUGHT` |
| `/console` | `console.error` on load | `E_CONSOLE_ERROR` |
| `/overflow` | 1400px block at 1280px viewport | `E_OVERFLOW_X` |
| `/unlabeled` | text input with no label | `E_UNLABELED_CONTROL` |
| `/missing` | link to a 404 document | `E_DEAD_LINK` (and `E_HTTP_4XX` on the page) |
| `/soft-404` | link target answers 200 with "Page not found" | `E_DEAD_LINK` with `soft_404: true` |
| `/old-route` | linked route redirects to `/dashboard` | `E_NOT_DEEPLINKABLE` |
| `/dropdown` | option label is a UUID | `E_UUID_EXPOSED` |
| `/dropdown` | option values are UUIDs | `E_UUID_IN_OPTION_VALUE` |
| `/dropdown` | enabled select with only a placeholder | `E_EMPTY_SELECT` |
| `/broken-api` | button click → `/api/boom` 500 | `E_500_ON_ACTION` |
| `/dashboard` | valid order submit → 500 | `E_500_ON_VALID` |
| `/dashboard` | "Delete order" control is quarantined | `E_DESTRUCTIVE_SKIPPED`, `kind: delete` |
| `/dashboard` | "Publish report" control is quarantined | `E_DESTRUCTIVE_SKIPPED`, `kind: side_effect` |
| `/novalidate` | `formnovalidate` on a required form | `E_VALIDATION_NOT_ENFORCED` |
| `/garbage` | `undefined`/`NaN` in prose, broken image, unhandled rejection, clickable div, focus ring removed | `E_RENDERED_GARBAGE`, `E_BROKEN_IMAGE`, `E_UNHANDLED_REJECTION`, `E_A11Y_SERIOUS`, `E_KEYBOARD_UNREACHABLE`, `E_NO_FOCUS_INDICATOR` |
| `/trap` | Tab handler that refocuses the field | `E_KEYBOARD_TRAP` (this page only) |
| `/code-sample` | `undefined`/`NaN` inside `code` and `pre` | none (control) |
| `/wide` | 700px block | `E_OVERFLOW_X` at 390x800 only |
| `/orphan` | in `sitemap.xml`, nothing links to it | `E_ORPHAN_ROUTE` |
| `/admin` | viewer role may load it though `deny` lists it | `E_AUTHZ_REACHABLE` (role `viewer`) |
| `/throttled` | 429 with `Retry-After: 1`, then 200 | `E_RATE_LIMITED`, not persistent |
| `/unstable` | repaints every 50ms (unlinked; its own visual run in the canary) | reported under not-tested as unstable, never `E_VISUAL_DIFF` |
| `/admin` as viewer | loads for a role whose `deny` lists it | `E_AUTHZ_REACHABLE` |
| `/dashboard` as viewer | admin-only control absent | `E_ROLE_AFFORDANCE_DIFF`, info |
| `/login` | none |: |

`/deck` and `/form-ok` are controls: they must produce **no** finding at all.

## What a false clean looks like

If the run reports zero findings, check in this order:

1. Did login actually happen? Zero auth redirects with `--auth form` means the tool
   never saw `/dashboard`; a report with pages = 1 is the auth-wall signature.
2. Did the crawler reach `/crash`? The nav links to it, so a missing page is a
   discovery bug.
3. Did `/` produce `E_DEAD_CONTROL`? If not, the action probe classified a no-op as
   something else: inspect `effect` on the probe row.
