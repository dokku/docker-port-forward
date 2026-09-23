# Command Reference

## Synopsis

```bash
# As a Docker CLI plugin:
docker pf TARGET [[[ADDRESS:]LOCAL_PORT:]REMOTE_PORT ...] [flags]

# Direct invocation:
docker-port-forward port-forward TARGET [[[ADDRESS:]LOCAL_PORT:]REMOTE_PORT ...] [flags]
```

## Arguments

| Argument | Required | Description |
| ---------- | ---------- | ------------- |
| `target` | Yes | The container or Compose service to forward to. See [Target Resolution](target-resolution.md). |
| `ports` | Optional | Port specs in `[[ADDRESS:]LOCAL_PORT:]REMOTE_PORT[/udp]` form. See [Port specification](#port-specification). If omitted, listening ports are auto-detected from the target. |

## Port specification

Each port spec follows the same form as `kubectl port-forward`, extended with `docker run -p` style bind addresses and an optional `/tcp` or `/udp` protocol suffix:

| Form | Meaning |
| ---- | ------- |
| `REMOTE` | Use the same port number for local and remote, TCP. |
| `LOCAL:REMOTE` | Listen on `LOCAL` locally, forward to `REMOTE` in the container, TCP. |
| `:REMOTE` | Let the OS pick a free local port; the chosen port is logged on startup. |
| `REMOTE/udp` | Same port both sides, UDP. |
| `LOCAL:REMOTE/udp` | Explicit local, UDP. |
| `:REMOTE/udp` | Auto local, UDP. |
| `ADDRESS:LOCAL:REMOTE` | Listen on `LOCAL` on `ADDRESS` only, instead of the `--address` list. |
| `ADDRESS::REMOTE` | Let the OS pick a free local port on `ADDRESS`. |
| `[IPV6]:LOCAL:REMOTE` | IPv6 addresses must be enclosed in brackets, e.g. `[::1]:8080:80`. |
| `*:LOCAL:REMOTE` or `:LOCAL:REMOTE` | Listen on `LOCAL` on every IPv4 and IPv6 interface, like `docker run -p LOCAL:REMOTE`. The empty-address form is Docker's. |
| `*::REMOTE` or `::REMOTE` | Let the OS pick a free local port, published on every interface. |

`ADDRESS` must be an IP literal, `localhost` (which binds both `127.0.0.1` and `::1`), or `*` (every IPv4 and IPv6 interface). `0.0.0.0` binds IPv4 interfaces only. Specs without an address are bound on every `--address`. Each spec with an address may also carry a protocol suffix, e.g. `0.0.0.0:5353:53/udp`.

The protocol suffix is case-insensitive. An omitted suffix defaults to TCP. Only `tcp` and `udp` are accepted; other suffixes (`sctp`, `icmp`, …) are rejected.

TCP and UDP may be mixed in a single invocation:

```bash
docker pf my-container 8080:80 53:53/udp
```

Multiple port specs may be provided. If no port specs are given, the command probes the target for listening sockets (TCP + UDP) and forwards each on the same host port. See [Auto-detection](#auto-detection).

## Flags

| Flag | Type | Default | Description |
| ------ | ------ | --------- | ------------- |
| `--address` | string (repeatable, comma-separated) | `localhost` | Addresses to listen on for port specs without their own address. Each must be an IP literal, `localhost` (binds both `127.0.0.1` and `::1`), or `*` (binds every IPv4 and IPv6 interface, like `docker run -p` with no host IP; can't be combined with other addresses). `0.0.0.0` binds IPv4 interfaces only. |
| `--container-running-timeout` | duration | `1m` | How long to wait for the helper container to be running before giving up. |
| `-d, --detach` | bool | `false` | Start the helper in the background and return immediately. The helper keeps running until removed with [`cleanup`](#port-forward-cleanup). |
| `--env-file` | string (repeatable) | | Path to an environment file for Compose interpolation. Only used when `TARGET` requires Compose resolution. |
| `-f, --file` | string (repeatable) | auto-detect | Path to a Compose file. Only used when `TARGET` requires Compose resolution. |
| `--helper-image` | string | `alpine/socat` | Image used for the sidecar helper container. See [Helper Image](helper-image.md). |
| `--label` | string (repeatable) | | Extra label to apply to the helper container, in `key=value` form. Repeat to add multiple. |
| `--log-driver` | string | daemon default | Logging driver for the container. Same as `docker container create --log-driver`. Applies to the helper only, not the short-lived auto-detect probe. |
| `--log-opt` | string (repeatable) | | Log driver options, in `key=value` form. Same as `docker container create --log-opt`. Not allowed with `--log-driver none`. |
| `--name` | string | auto-generated | Name to assign to the helper container. When omitted, a name like `port-forward-<target>-<rand>` is generated. Use the name with `cleanup --name` to remove a specific forward. |
| `--profile` | string (repeatable) | | One or more Compose profiles to enable when resolving services. |
| `--project-directory` | string | | Alternate Compose project directory. |
| `-p, --project-name` | string | directory name | Compose project name; used when resolving `service/` or bare-name targets. |
| `--pull` | string | `missing` | Pull policy for the helper image: `always`, `missing`, or `never`. |
| `--restart` | string | `unless-stopped` with `--detach`, `no` otherwise | Restart policy to apply when a container exits. Takes the same values as `docker container create --restart`: `no`, `always`, `unless-stopped`, or `on-failure[:max-retries]`. Any value other than `no` requires `--detach`, because attached helpers are auto-removed. See [Helper Image](helper-image.md#restart-policy). |
| `--skip-preflight` | bool | `false` | Skip the [preflight host-port check](#preflight-host-port-check). Needed to forward ports below 1024 when the plugin isn't running as root. |
| `--tcp-half-close-timeout` | duration | `0` (socat's `0.5s`) | How long each TCP forward waits for the other side after one side closes its write half (`socat -t`). Raise it for clients that half-close and then wait for a reply; the old dokku ambassador used `100000000s`. Must not be negative. |
| `--udp-timeout` | duration | `60s` | Idle timeout for UDP pseudo-sessions inside the helper (`socat -T` for every UDP forward). Ignored when the invocation has no UDP pairs. |

## Auto-detection

When no port specs are supplied, the command starts a short-lived probe container in the target's network namespace, reads `/proc/net/{tcp,tcp6,udp,udp6}`, and forwards every non-loopback listener it finds on the same host port.

- TCP listeners are identified by state `0A` (TCP_LISTEN).
- UDP "listeners" are bound UDP sockets in state `07` (TCP_CLOSE, the kernel's term for a bound, unconnected UDP socket — what `ss -uln` shows).
- Listeners bound only to `127.0.0.1` or `::1` are **skipped** — the helper-publish architecture cannot reach loopback-only sockets (see [Helper Image](helper-image.md#loopback-limitation)).

## Idempotency

If a running helper for the same target already covers any of the requested `(local, remote)` pairs, the command prints the existing helper's identity and exits `0` without creating a new one. This makes it safe to re-run `docker pf ... --detach` from scripts. The existing helper is reused as-is, even if it was created with a different `--restart`, `--log-driver`, `--log-opt` or `--tcp-half-close-timeout` setting.

The exception is a stale helper, one that can no longer reach its target (see [`port-forward list`](#port-forward-list)). A stale helper is removed and replaced with a new one, so re-running the command fixes a forward whose target changed IP on the default `bridge` network. A running helper that holds a requested host port for a target container that no longer exists is also removed.

## Preflight host-port check

Before creating a helper, the command briefly tries to `Listen()` on each requested host port. If the bind fails with `EADDRINUSE`, the command errors out with a clear message. This catches conflicts before Docker would report an opaque publish error.

The check runs in the plugin's own process, so when the plugin isn't running as root it also rejects ports below 1024 with `permission denied`, even though the Docker daemon could publish them. Pass `--skip-preflight` to skip the check; any real conflict is then reported by Docker when the helper starts. Without the check, the default `localhost` addresses are published on `::1` even on hosts without IPv6, which fails; use `--address 127.0.0.1` or `127.0.0.1:` port specs there.

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

Run in the background without restarting the helper when it exits or the daemon restarts:

```bash
docker pf --detach --restart no my-db 5432:5432
```

Bind each port on its own address:

```bash
docker pf my-container 127.0.0.1:8080:80 0.0.0.0:5432:5432 [::1]:9000:9000/udp
```

Forward a port below 1024 without running the plugin as root:

```bash
docker pf --skip-preflight my-container 127.0.0.1:80:80
```

Send the helper's logs to a rotated JSON file:

```bash
docker pf --detach --log-driver json-file --log-opt max-size=10m my-container 8080:80
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

Bind all IPv4 and IPv6 interfaces, like `docker run -p 8080:80`:

```bash
docker pf --address '*' my-container 8080:80
docker pf my-container :8080:80
```

Bind all IPv4 interfaces only:

```bash
docker pf --address 0.0.0.0 my-container 8080:80
```

Keep TCP connections open after a client half-closes, like the old dokku ambassador:

```bash
docker pf --detach --tcp-half-close-timeout 100000000s my-container 8080:80
```

Forward to a Compose service with explicit compose files and project name:

```bash
docker pf -f docker-compose.yml -f docker-compose.dev.yml -p proj service/api 3000:3000
```

## Exit behavior

In attached mode the command blocks until it receives `SIGINT` (Ctrl-C) or `SIGTERM`, then stops and removes the helper container it created. In detached mode the helper survives after the CLI returns and is cleaned up only when a `cleanup` command removes it (or by `docker rm`). With the default `unless-stopped` restart policy, a detached helper is also restarted after it exits or the Docker daemon restarts, unless it was stopped explicitly.

## port-forward cleanup

Remove leftover helper sidecar containers.

```bash
docker pf cleanup [flags]
```

| Flag | Type | Default | Description |
| ------ | ------ | --------- | ------------- |
| `--dry-run` | bool | `false` | Print the helpers that would be removed without removing them. |
| `--name` | string | | Act on the single helper with this container name. Fails if the container exists but isn't a port-forward helper. |
| `--stale` | bool | `false` | Only act on stale helpers, those that can no longer reach their target. See [`port-forward list`](#port-forward-list). |
| `--target` | string | | Only act on helpers for the given target container id or name. Ignored when `--name` is set. |

Examples:

```bash
docker pf cleanup
docker pf cleanup --dry-run
docker pf cleanup --target my-container
docker pf cleanup --name port-forward-mydb-a9c2
docker pf cleanup --stale
```

The command prints one line per matching helper (`<short-id>  name=<name> target=<target-short-id> ports=<ports>`, followed by `stale="<reason>"` for stale helpers) and a summary. Exit code is zero when all matching helpers were removed (or when none were found), non-zero when some removals failed.

Manual fallback:

```bash
docker ps -aq --filter 'label=com.dokku.port-forward=true' | xargs -r docker rm -f
```

## port-forward list

List helper sidecar containers and whether they can still reach their target.

```bash
docker pf list [flags]
```

| Flag | Type | Default | Description |
| ------ | ------ | --------- | ------------- |
| `--name` | string | | Show the single helper with this container name. Fails if the container exists but isn't a port-forward helper. |
| `--stale` | bool | `false` | Only show stale helpers. |
| `--target` | string | | Only show helpers for the given target container id or name. Ignored when `--name` is set. |

Each row has the form `<short-id>  name=<name> target=<target-short-id> ports=<ports> bindings=<bindings> network=<network> address=<address>`, where `network` and `address` are the network the helper shares with the target and what it dials there. Stale helpers end with `stale="<reason>"`.

A helper is stale when:

- The helper dials a container name (user-defined networks) and no container with that name exists, or it is no longer attached to the network.
- The helper dials an IP (the default `bridge` network) and the target container no longer exists, is no longer attached to the network, or is running with a different IP.

A stopped target is not stale. Helpers created by versions that didn't record the network and address are never reported stale.

Re-running `docker pf ... --detach` replaces a stale helper that covers the requested ports (see [Idempotency](#idempotency)), and `docker pf cleanup --stale` removes every stale helper.

## See also

- [Target Resolution](target-resolution.md) -- how `container/`, `service/`, and bare-name targets are resolved
- [Compose Integration](compose-integration.md) -- details on Compose flags and service lookup
- [Helper Image](helper-image.md) -- the sidecar container that handles the actual proxy
- [Library Usage](library-usage.md) -- using the same functionality from Go code
