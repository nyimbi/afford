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
| `/` | `target="_blank"` link | `E_NEW_TAB` |
| `/form-ok` | `<input type="file">` | `E_UNSUPPORTED_INPUT` |
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
| `/login` | none | — |

`/deck` and `/form-ok` are controls: they must produce **no** finding at all.

## What a false clean looks like

If the run reports zero findings, check in this order:

1. Did login actually happen? Zero auth redirects with `--auth form` means the tool
   never saw `/dashboard`; a report with pages = 1 is the auth-wall signature.
2. Did the crawler reach `/crash`? The nav links to it, so a missing page is a
   discovery bug.
3. Did `/` produce `E_DEAD_CONTROL`? If not, the action probe classified a no-op as
   something else — inspect `effect` on the probe row.
