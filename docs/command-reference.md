# Command Reference

## Synopsis

```bash
# As a Docker CLI plugin:
docker pf TARGET [[LOCAL_PORT:]REMOTE_PORT ...] [flags]

# Direct invocation:
docker-port-forward port-forward TARGET [[LOCAL_PORT:]REMOTE_PORT ...] [flags]
```

## Arguments

| Argument | Required | Description |
| ---------- | ---------- | ------------- |
| `target` | Yes | The container or Compose service to forward to. See [Target Resolution](target-resolution.md). |
| `ports` | Optional | Port specs in `[LOCAL_PORT:]REMOTE_PORT` form. See [Port specification](#port-specification). If omitted, listening ports are auto-detected from the target. |

## Port specification

Each port spec follows the same form as `kubectl port-forward`, with an optional Docker-style `/tcp` or `/udp` protocol suffix:

| Form | Meaning |
| ---- | ------- |
| `REMOTE` | Use the same port number for local and remote, TCP. |
| `LOCAL:REMOTE` | Listen on `LOCAL` locally, forward to `REMOTE` in the container, TCP. |
| `:REMOTE` | Let the OS pick a free local port; the chosen port is logged on startup. |
| `REMOTE/udp` | Same port both sides, UDP. |
| `LOCAL:REMOTE/udp` | Explicit local, UDP. |
| `:REMOTE/udp` | Auto local, UDP. |

The protocol suffix is case-insensitive. An omitted suffix defaults to TCP. Only `tcp` and `udp` are accepted; other suffixes (`sctp`, `icmp`, …) are rejected.

TCP and UDP may be mixed in a single invocation:

```bash
docker pf my-container 8080:80 53:53/udp
```

Multiple port specs may be provided. If no port specs are given, the command probes the target for listening sockets (TCP + UDP) and forwards each on the same host port. See [Auto-detection](#auto-detection).

## Flags

| Flag | Type | Default | Description |
| ------ | ------ | --------- | ------------- |
| `--address` | string (repeatable, comma-separated) | `localhost` | Addresses to listen on. `localhost` binds both `127.0.0.1` and `::1`. `0.0.0.0` binds all IPv4 interfaces. |
| `--container-running-timeout` | duration | `1m` | How long to wait for the helper container to be running before giving up. |
| `-d, --detach` | bool | `false` | Start the helper in the background and return immediately. The helper keeps running until removed with [`cleanup`](#port-forward-cleanup). |
| `--env-file` | string (repeatable) | | Path to an environment file for Compose interpolation. Only used when `TARGET` requires Compose resolution. |
| `-f, --file` | string (repeatable) | auto-detect | Path to a Compose file. Only used when `TARGET` requires Compose resolution. |
| `--helper-image` | string | `alpine/socat` | Image used for the sidecar helper container. See [Helper Image](helper-image.md). |
| `--label` | string (repeatable) | | Extra label to apply to the helper container, in `key=value` form. Repeat to add multiple. |
| `--name` | string | auto-generated | Name to assign to the helper container. When omitted, a name like `port-forward-<target>-<rand>` is generated. Use the name with `cleanup --name` to remove a specific forward. |
| `--profile` | string (repeatable) | | One or more Compose profiles to enable when resolving services. |
| `--project-directory` | string | | Alternate Compose project directory. |
| `-p, --project-name` | string | directory name | Compose project name; used when resolving `service/` or bare-name targets. |
| `--pull` | string | `missing` | Pull policy for the helper image: `always`, `missing`, or `never`. |
| `--udp-timeout` | duration | `60s` | Idle timeout for UDP pseudo-sessions inside the helper (`socat -T` for every UDP forward). Ignored when the invocation has no UDP pairs. |

## Auto-detection

When no port specs are supplied, the command starts a short-lived probe container in the target's network namespace, reads `/proc/net/{tcp,tcp6,udp,udp6}`, and forwards every non-loopback listener it finds on the same host port.

- TCP listeners are identified by state `0A` (TCP_LISTEN).
- UDP "listeners" are bound UDP sockets in state `07` (TCP_CLOSE, the kernel's term for a bound, unconnected UDP socket — what `ss -uln` shows).
- Listeners bound only to `127.0.0.1` or `::1` are **skipped** — the helper-publish architecture cannot reach loopback-only sockets (see [Helper Image](helper-image.md#loopback-limitation)).

## Idempotency

If a running helper for the same target already covers any of the requested `(local, remote)` pairs, the command prints the existing helper's identity and exits `0` without creating a new one. This makes it safe to re-run `docker pf ... --detach` from scripts.

## Preflight host-port check

Before creating a helper, the command briefly tries to `Listen()` on each requested host port. If the bind fails with `EADDRINUSE`, the command errors out with a clear message. This catches conflicts before Docker would report an opaque publish error.

## Examples

Forward a single port:

```bash
docker pf my-container 8080:80
```

Forward using the same local and remote port:

```bash
docker pf my-container 5000
```

Let the OS pick a local port:

```bash
docker pf my-container :5000
```

Forward several ports at once:

```bash
docker pf my-container 8080:80 5432:5432 :6379
```

Auto-detect every non-loopback listener in the container (TCP + UDP) and forward each on the same host port:

```bash
docker pf my-container
```

Forward a UDP port (DNS):

```bash
docker pf my-dns 5353:53/udp
```

Mix TCP and UDP in one invocation:

```bash
docker pf my-app 8080:80 53:53/udp
```

Increase UDP idle timeout for a chatty forward:

```bash
docker pf --udp-timeout 10m my-app 53:53/udp
```

Run in the background and give the helper an explicit name:

```bash
docker pf --detach --name mydb my-db 5432:5432
```

Add extra labels to the helper container (useful for your own `docker ps --filter` queries):

```bash
docker pf --label team=backend --label env=dev my-container 8080:80
```

Forward to a specific container by ID:

```bash
docker pf container/abc123 8080:80
```

Forward to a Compose service by name:

```bash
docker pf service/web 8080:80
```

Bind all interfaces:

```bash
docker pf --address 0.0.0.0 my-container 8080:80
```

Forward to a Compose service with explicit compose files and project name:

```bash
docker pf -f docker-compose.yml -f docker-compose.dev.yml -p proj service/api 3000:3000
```

## Exit behavior

In attached mode the command blocks until it receives `SIGINT` (Ctrl-C) or `SIGTERM`, then stops and removes the helper container it created. In detached mode the helper survives after the CLI returns and is cleaned up only when a `cleanup` command removes it (or by `docker rm`).

## port-forward cleanup

Remove leftover helper sidecar containers.

```bash
docker pf cleanup [flags]
```

| Flag | Type | Default | Description |
| ------ | ------ | --------- | ------------- |
| `--dry-run` | bool | `false` | Print the helpers that would be removed without removing them. |
| `--name` | string | | Act on the single helper with this container name. Fails if the container exists but isn't a port-forward helper. |
| `--target` | string | | Only act on helpers for the given target container id or name. Ignored when `--name` is set. |

Examples:

```bash
docker pf cleanup
docker pf cleanup --dry-run
docker pf cleanup --target my-container
docker pf cleanup --name port-forward-mydb-a9c2
```

The command prints one line per matching helper (`<short-id>  name=<name> target=<target-short-id> ports=<ports>`) and a summary. Exit code is zero when all matching helpers were removed (or when none were found), non-zero when some removals failed.

Manual fallback:

```bash
docker ps -aq --filter 'label=com.dokku.port-forward=true' | xargs -r docker rm -f
```

## See also

- [Target Resolution](target-resolution.md) -- how `container/`, `service/`, and bare-name targets are resolved
- [Compose Integration](compose-integration.md) -- details on Compose flags and service lookup
- [Helper Image](helper-image.md) -- the sidecar container that handles the actual proxy
