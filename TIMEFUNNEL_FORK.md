# Timefunnel Fork Maintenance

This fork keeps MediaStationGo close to upstream while carrying a small set of
deployment-specific fixes for the Timefunnel media stack.

## Repository Layout

- `origin`: `https://github.com/timefunnel/MediaStationGo.git`
- `upstream`: `https://github.com/ShukeBta/MediaStationGo.git`
- maintenance branch: `codex/timefunnel-msg-fork`
- production image target: `ghcr.io/timefunnel/mediastation-go:<tag>`

## Maintenance Rules

1. Keep custom changes small and documented.
2. Prefer feature flags or configuration over hard-coded environment behavior.
3. Do not rewrite unrelated upstream code while fixing a local issue.
4. Add or update tests for every behavior change.
5. Sync upstream by merging or cherry-picking intentionally; do not overwrite
   local patches.

## Initial Patch Scope

Priority 1 fixes:

- Make OpenList playback resolve more tolerant of stale object cache by listing
  the parent directory before retrying `fs/get` when OpenList reports a false
  negative missing-file error.
- Keep cloud playback as pure 302 whenever OpenList can provide a CDN URL.
- Improve Emby-compatible playback metadata for Filmly/Infuse/VidHub without
  forcing server-side transcoding.
- Align recycle-bin or deleted-media behavior with OpenList Meta Hide so removed
  entries do not reappear on the next scan.

Priority 2 fixes:

- Add precise root-level scan and single-item scrape APIs for bot integration.
- Add library migration without re-scraping when only library/path ownership
  changes.
- Improve adult title normalization, poster/thumb handling, and subtitle status
  reporting.

## Baseline Validation

Before deploying a custom image:

```bash
go vet ./...
go build ./...
go test ./...
cd web && npm ci && npm run build
docker compose -f docker-compose.yml config --quiet
```

The local Windows workstation currently does not have Go or Docker installed, so
baseline validation should run in GitHub Actions or on a controlled remote build
host.

## Production Deployment Rule

Production compose should only switch from upstream to the fork after:

1. CI passes on the fork branch.
2. A tagged GHCR image is published.
3. `/data/MediaStationGo/data` and `/data/MediaStationGo/postgres` are backed up.
4. A rollback image tag is recorded.
