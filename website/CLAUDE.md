# website (Laredo docs site)

A small, hand-rolled static site generator for the Laredo documentation
(`unified`/remark/rehype). Deployed to GitHub Pages at `https://zrz.io/laredo/`
via `.github/workflows/deploy-docs.yml` on pushes to `main` that touch
`website/**` or `docs/edr/**`.

No framework, no client-side JS for the docs themselves (only the interactive
diagrams use canvas). The build is `node build.mjs`.

## Build & preview

```bash
cd website
pnpm install              # uses pnpm (pinned via packageManager; corepack picks it up)
pnpm run build            # node build.mjs → website/dist/
pnpm run serve            # preview at http://localhost:8080/laredo/ (strips the base path)
```

Package manager is **pnpm** (pinned to `pnpm@10.33.2` via `packageManager`).
`website/.npmrc` sets `minimum-release-age=2880` — a **48-hour cooldown**: a newly
published dependency version must be at least 48h old before pnpm will install
it (supply-chain safety). CI installs with `pnpm install --frozen-lockfile`.
Commit `pnpm-lock.yaml`; do not reintroduce `package-lock.json`.

The site is served under the base path `/laredo` — every internal link/asset is
prefixed with it (the generator handles this via the `BASE` constant; the
preview server strips it). Output is `website/dist/`.

## Layout

| Path | Role |
|---|---|
| `build.mjs` | The generator: renders docs + EDRs, sidebar, EDR listing, landing, copies static assets. |
| `serve.mjs` | Local preview server. |
| `templates/` | `layout.html` (chrome), `home.html` (landing), `doc.html` (sidebar + body + TOC), `edr.html`, `index.html` (EDR listing). `{{var}}` substitution. |
| `static/styles.css` | The single stylesheet (system fonts, warm/amber palette, light + dark via CSS vars). |
| `static/viz/` | Self-contained `<canvas>` + vanilla-JS interactive diagrams + `viz.css`. `?embed=1` hides chrome for iframing. |
| `static/img/` | Favicon and images (copied as-is). |
| `docs/` | Markdown doc pages, grouped by category directory. |
| `../docs/edr/` | EDRs (`NNNN-slug.md`) — rendered into the site's Decision Records section. |

## Adding content

- **A doc page**: create `docs/<category>/<name>.md` with frontmatter
  `title` (required) and `sidebar_position` (orders within the category). The
  category directory must be one of the `CATEGORIES` in `build.mjs` (Getting
  Started, Concepts, Guides, Reference, Design, Internals, Operations) — add a
  new entry there to introduce a new category. The sidebar is generated
  automatically; there is no separate sidebar file.
- **An EDR**: create `../docs/edr/NNNN-slug.md` with the EDR frontmatter
  (`id`, `title`, `status` [proposed|accepted|deprecated|superseded], `date`,
  `authors`, `tags`; `proposed` requires `proposed_until`). `id` must match the
  filename number. It appears in the listing and gets pretty/redirect URLs.
- **An interactive diagram**: add `static/viz/<name>.html` (copy an existing one
  as a template — canvas + the shared `viz.css`, with `?embed` detection), link
  it from `static/viz/index.html`, and embed it in a doc with
  `<iframe src="/laredo/viz/<name>.html?embed=1" class="embed"></iframe>`.

## Linking

Author links as relative `.md` (so they also work on GitHub) — e.g.
`[fan-out](../guides/fan-out.md)`. The generator rewrites them to base-prefixed
pretty URLs. Site-absolute links (`/guides/x`) are also base-prefixed. ASCII
diagrams in code fences render fine; richer concepts also get a linked diagram.

## Keeping docs in sync

**Critical.** When laredo code changes, update the matching docs — stale docs are
worse than none. Triggers (page to update):

- Config options → `docs/reference/configuration.md`
- CLI changes → `docs/reference/cli.md`
- gRPC API → `docs/reference/grpc-api.md`
- Go library API → `docs/getting-started/library-usage.md` + relevant pages
- New source/target → `docs/concepts/{sources,targets}.md` + a guide page
- Health/metrics → `docs/reference/{health-endpoints,metrics}.md`
- Engine/pipeline/ordering → `docs/concepts/{pipelines,ordering-and-delivery}.md`
- Snapshots → `docs/concepts/snapshots.md`, `docs/guides/snapshots.md`
- Fan-out protocol → `docs/guides/fan-out.md`
- Snapshot writer → `docs/guides/snapshot-writer.md`, `docs/design/snapshot-writer.md`, `../docs/edr/0001-snapshot-writer.md`
- Architecture → `docs/concepts/architecture.md`
- New major feature → add a doc page (in a `CATEGORIES` directory) and, for a
  design decision, an EDR under `../docs/edr/`.

The root `README.md` is the concise quick-reference — update it when the public
interface changes; the website goes deeper.
