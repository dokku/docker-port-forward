# Helper Image

Unlike `kubectl port-forward`, which tunnels through the Kubernetes API server's streaming endpoints, Docker has no equivalent API for publishing ports on a running container after the fact. `docker-port-forward` gets around this by starting a small sidecar helper container on one of the target's networks that publishes the requested host ports and proxies traffic to the target.

## How it works

When a forward starts, the plugin:

1. Ensures the helper image is available (see [Pull policy](#pull-policy) below).
2. Inspects the target to pick a network both it and the helper can share (a user-defined network is preferred; otherwise the default `bridge`) and learns the target's IP address on that network.
3. Verifies each requested host port is currently free on each requested address.
4. Looks for an existing helper for the same target whose labels say it already covers any of the requested `(local, remote)` pairs. If one is found, the command exits `0` without creating a new helper (see [Idempotency](command-reference.md#idempotency)).
5. Creates a container from the helper image with:
   - `--network <target-network>` and `-p <addr>:<local>:<remote>` for every requested port pair and address.
   - A shell command that launches one `socat TCP-LISTEN:<remote>,fork,reuseaddr TCP:<target-ip>:<remote>` per distinct remote port, supervised by `sh -c` with a `trap 'kill 0' EXIT` so a dying socat brings down the helper.
   - Labels `com.dokku.port-forward=true`, `target=<id>`, `session=<uuid>`, `name=<container-name>`, `ports=<encoded-pairs>`, `addresses=<addr-list>`, plus any user-supplied `--label` values.
   - `AutoRemove: true` for attached mode; `AutoRemove: false` for `--detach` (so the helper survives until explicitly removed).

When the CLI is attached and receives `SIGINT`/`SIGTERM`, it stops and removes the helper. In detached mode the helper keeps running until it is removed via `docker port-forward cleanup` or plain `docker rm`.

## Auto-detection

When no port specs are given, the command briefly starts a second short-lived probe container with `--network container:<target-id>` and reads `/proc/net/tcp`/`/proc/net/tcp6` through `cat`. That file is namespace-local, so the probe sees the target's listeners. Each non-loopback listener becomes a `<port>:<port>` pair for the actual forward. The probe is removed when the read completes.

## Loopback limitation

Because the helper attaches to a network and publishes host ports via `-p`, it cannot share the target's network namespace — Docker does not allow combining `--network container:X` with `-p`. As a result, any service inside the target that is bound only to `127.0.0.1` (or `::1`) is unreachable through `docker port-forward`. Such listeners are silently skipped during auto-detection; if you pass them explicitly they will appear to start but fail to connect because the target-side socket is not listening on the network interface.

The typical remediation is to rebind the service inside the container to `0.0.0.0` (or the container's network interface) during development. For the full matrix of unsupported network modes, see [Limitations](limitations.md).

## Default image: `alpine/socat`

The default helper is [`alpine/socat`](https://hub.docker.com/r/alpine/socat), a small (~5 MB) image maintained by the Alpine Linux team. The plugin overrides the image's entrypoint (`socat`) with `sh -c "..."` so a shell can supervise one socat process per distinct remote port.

## Pull policy

The `--pull` flag controls how the helper image is obtained:

| Value | Behavior |
| ----- | -------- |
| `missing` (default) | Pull only if the image is not already present locally. |
| `always` | Pull on every invocation. Useful to pick up helper image updates. |
| `never` | Never pull. Fail early if the image is missing locally. Useful in air-gapped environments. |

## Overriding the helper image

Use `--helper-image` to point at any image that has `socat` and `sh` on `PATH`:

```bash
docker port-forward --helper-image registry.internal/alpine/socat:latest my-container 8080:80
```

The plugin only requires:

- `socat` must be on `PATH`.
- `sh` must be on `PATH` (so multiple port forwards can share one container).

## Extra labels

`--label key=value` (repeatable) adds arbitrary labels to the helper container. Combined with the plugin's built-in labels, this is handy for grouping forwards and writing cleanup filters like:

```bash
docker port-forward --detach --label team=backend --label env=dev my-db 5432:5432
docker ps --filter 'label=team=backend'
```

## Cleaning up stale helpers

If the plugin crashes, an orphaned helper container can be left running. Use the `cleanup` subcommand to sweep orphans explicitly:

```bash
docker port-forward cleanup                          # remove all helpers
docker port-forward cleanup --dry-run                # list without removing
docker port-forward cleanup --target my-container    # scoped to one target
docker port-forward cleanup --name port-forward-mydb-a9c2  # remove one by name
```

See the [Command Reference](command-reference.md#port-forward-cleanup) for flag details.

If the plugin itself is unavailable, you can fall back to plain Docker:

```bash
docker ps -aq --filter 'label=com.dokku.port-forward=true' | xargs -r docker rm -f
```
