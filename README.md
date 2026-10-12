# blocky-ctrl

A remote control for [blocky](https://github.com/0xERR0R/blocky) DNS: pause and resume blocking on one or more instances from your phone, and see a few numbers while you're there. One Go binary, UI embedded, no build step, no database.

It is deliberately small. For graphs and query history, point Grafana at blocky's Prometheus endpoint.

Each instance gets a card with:

- **Pause / resume**: tap the shield to pause for 5 min, 15 min, 1 hour or until resumed, for all groups or only some. While paused it shows the countdown; tap again to resume.
- **Last 24 hours** from blocky's `/api/stats`: queries, share blocked, share cached, average response time and queries per hour
- **Details**: most blocked domains, busiest clients, allowlist/denylist sizes per group, refresh lists, flush cache

## Config

```yaml
instances:
  - name: Home DNS
    url: http://10.0.0.53:4000
  - name: Backup
    url: http://10.0.0.54:4000
    readonly: true
    label: Failover
```

`url` is blocky's `ports.http`. Credentials in the URL (`http://user:pass@host:4000`) are sent as basic auth, for blocky behind a reverse proxy; they are never shown in the UI.

A `readonly` instance shows its state and stats but has no pause button, and the server refuses to toggle it. Use it for instances that get their blocking state from another one through blocky's `redis` setting, like failovers or load balanced replicas. `label` sets the badge text (default `readonly`). Refresh lists and flush cache still work on it.

Requires blocky v0.35+ with `statistics.enable: true`. An instance without it shows as an error.

## Run

```sh
blocky-ctrl --config blocky-ctrl.yml --bind 0.0.0.0 --port 8080
```

| Flag | Default | |
|---|---|---|
| `-c`, `--config` | `blocky-ctrl.yml` | config file |
| `-b`, `--bind` | all interfaces | address to bind to |
| `-p`, `--port` | `8080` | port to listen on |
| `-v`, `--version` | | print the version and exit |

### Docker

```sh
docker run -d -p 8080:8080 -v ./blocky-ctrl.yml:/config/blocky-ctrl.yml:ro ghcr.io/mxlje/blocky-ctrl:0.0
```

Tags: `latest`, `0.0` (latest patch) and exact versions like `0.0.2`, for linux/amd64, arm64 and arm/v7. `just image` builds it locally. The image is the static binary on distroless (about 11 MB) and runs as a non-root user. It has a health check (`blocky-ctrl healthcheck`, which calls `/healthz`). Map ports with `-p` rather than changing `--port`, or the health check won't find the server.

There is no login. Run it on a trusted network or behind a reverse proxy with auth. Cross-site POSTs are rejected, so other web pages can't toggle blocking through your browser.

On a phone, "Add to Home Screen" gives it an icon and opens it full screen.

Icons are from [Lucide](https://lucide.dev) (ISC); the licence ships in the binary at `/third-party-licenses.txt`.

## Develop

```sh
just dev-up    # two blocky v0.35 instances + Redis in OrbStack, the second one read-only
just traffic   # send some queries to both
just dev       # http://127.0.0.1:18090
just test
just dist      # linux/darwin binaries in dist/
```

### Release

Push a `v*` tag:

```sh
git tag -a v0.1.0 -m v0.1.0
git push origin v0.1.0
```

`.github/workflows/release.yml` then runs the tests, pushes the image to `ghcr.io/mxlje/blocky-ctrl` as `0.1.0`, `0.1` and `latest` (linux/amd64, arm64, arm/v7), and creates the GitHub release with the binaries, `SHA256SUMS` and generated notes. The release is only created once the image is pushed. If a step fails, delete the tag (`git push --delete origin v0.1.0 && git tag -d v0.1.0`), fix, and tag again.

## About

Claude wrote this, with me steering. It does exactly what I wanted, and that's the whole bar. You're welcome to use it, fork it or ignore it, but I won't be acting on complaints or feature requests. No hard feelings either way.

## License

MIT, see [LICENSE](LICENSE). Icons are from Lucide, see above.
