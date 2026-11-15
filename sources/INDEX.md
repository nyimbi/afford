# Sources

Third-party material afford ships or relies on, archived here so a citation never
points at a URL that has moved.

| file | what | version | licence | origin | retrieved |
|---|---|---|---|---|---|
| `axe.min.js` | axe-core accessibility rule engine, embedded and injected by `internal/pass` | 4.10.2 | MPL-2.0 | https://cdnjs.cloudflare.com/ajax/libs/axe-core/4.10.2/axe.min.js | 2026-11-15 |

axe-core is distributed under the Mozilla Public License 2.0. The file is shipped
unmodified; its licence header is retained in the file. Upstream:
https://github.com/dequelabs/axe-core.

## Behaviour afford relies on

- `axe.run(document, { runOnly, rules })` resolves to `{ violations: [{ id, impact, help, helpUrl, nodes: [{ target, html }] }] }`. `impact` is one of `minor`, `moderate`, `serious`, `critical`. afford maps `serious` and `critical` to major findings and the rest to minor.
- Rules afford turns off by default, because another check already owns them: `label` (`E_UNLABELED_CONTROL`) and `document-title` (`E_NO_TITLE`).
- Updating: replace the file, change the version here, run `make canary`. The canary plants an unlabeled image and a low-contrast control, so a silent API change fails it.
