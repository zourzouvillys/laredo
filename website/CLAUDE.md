# website (Docusaurus docs site)

Docusaurus 3.9.2 site for Laredo documentation. Deployed to GitHub Pages via `.github/workflows/deploy-docs.yml` on pushes to `main` that touch `website/**`.

## Build

```bash
cd website
npm ci
npm run build     # production build into website/build/
npm start         # local dev server with hot reload
```

## Doc Structure

Docs live in `website/docs/` as Markdown. Sidebar order is defined in `sidebars.ts`.

| Section | Path | Covers |
|---|---|---|
| Intro | `docs/intro.md` | What Laredo is, high-level overview |
| Getting Started | `docs/getting-started/` | Quick start, library usage, Docker |
| Concepts | `docs/concepts/` | Architecture, pipelines, sources, targets, snapshots, ordering & delivery |
| Guides | `docs/guides/` | PostgreSQL, in-memory targets, HTTP sync, fan-out, filters/transforms, snapshots, error handling, monitoring, Kubernetes |
| Reference | `docs/reference/` | Configuration, CLI, gRPC API, metrics, health endpoints |
| Operations | `docs/operations/` | Slot lag, re-baseline, dead letters, troubleshooting |

## Keeping Docs in Sync

**This is critical.** When making changes to laredo code, update the corresponding docs. Stale docs are worse than no docs.

### What triggers a doc update

- **New or changed config options**: update `docs/reference/configuration.md`
- **CLI command changes** (new subcommands, changed flags, new output): update `docs/reference/cli.md`
- **gRPC API changes** (new RPCs, changed request/response): update `docs/reference/grpc-api.md`
- **Go library API changes** (new interfaces, changed method signatures): update `docs/getting-started/library-usage.md` and relevant concept/guide pages
- **New source implementation**: add to `docs/concepts/sources.md`, add a guide page
- **New target implementation**: add to `docs/concepts/targets.md`, add a guide page
- **Health endpoint changes**: update `docs/reference/health-endpoints.md`
- **Metrics changes** (new metrics, changed labels): update `docs/reference/metrics.md`
- **Pipeline/engine behavior changes**: update `docs/concepts/pipelines.md`
- **Snapshot behavior changes**: update `docs/concepts/snapshots.md` and `docs/guides/snapshots.md`
- **Error handling changes**: update `docs/guides/error-handling.md`
- **PostgreSQL source changes** (publication management, reconnect, slot modes): update `docs/guides/postgresql.md`
- **Fan-out protocol changes**: update `docs/guides/fan-out.md`
- **Kubernetes/Docker deployment changes**: update `docs/guides/kubernetes.md` and `docs/getting-started/docker.md`
- **Architecture changes**: update `docs/concepts/architecture.md`
- **New major feature or concept**: add a new doc page and wire it into `sidebars.ts`

### README.md

The root `README.md` is the quick-reference for developers. When a change affects the public interface (new endpoints, CLI commands, config options, changed behavior), update the README too. The website docs go deeper; the README stays concise.

### Sidebar

When adding a new doc page, add it to `sidebars.ts` in the appropriate category. The sidebar controls navigation order.
