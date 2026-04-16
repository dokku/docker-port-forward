# Limitations and Unsupported Setups

`docker-port-forward` works by attaching a small helper container to one of the target's networks and publishing host ports that `socat` proxies to the target. That architecture is simple and robust for the common case of a container on a user-defined bridge or the default `bridge`, but it rules out several networking configurations. This page is the authoritative list of what does not work and what to do instead.

## Quick reference

| Scenario | Works? | Workaround |
| --- | --- | --- |
| Container on a user-defined bridge network | Yes | — |
| Container on the default `bridge` network | Yes | — |
| Container on `network_mode: host` | **No** | Use the host's own networking; no forward needed. |
| Container on `network_mode: none` | **No** | Attach the container to a bridge first. |
| Container on `network_mode: container:<other>` | Yes (via the other container's network) | — |
| Container on a `macvlan` or `ipvlan` network | **Sometimes** | See [Macvlan / ipvlan](#macvlan--ipvlan). |
| Container on a Swarm overlay network | **No** | Publish via `docker service update --publish-add`. |
| Remote Docker daemon (`DOCKER_HOST=tcp://…`) | **No (host-binding)** | Run the plugin on the host that runs the daemon. |
| Rootless Docker | **Mostly yes** | Host ports < 1024 unavailable to the daemon user. |
| Target service bound to `127.0.0.1` only | **No** | Rebind inside the container to `0.0.0.0`. |
| Target service bound to `::1` only | **No** | Same — rebind to `::` or add a public bind. |
| IPv6-only target networks | **No** | Attach the target to an IPv4-enabled network. |
| Privileged host ports (< 1024) | Works only as root | Use `LOCAL:REMOTE` with a high `LOCAL`. |
| Swarm services (replicas, routing mesh) | **No** | Use the routing mesh's published port instead. |
| UDP services | Yes | Suffix spec with `/udp` (e.g., `53:53/udp`). See [UDP forwarding](#udp-forwarding). |
| SCTP or other non-TCP/UDP protocols | **No** | Only TCP and UDP are supported. |

## Unsupported network modes

### `network_mode: host`

The target shares the host's network namespace and therefore has no container-network IP the helper could dial. Host-network containers already expose their ports directly on the host, so `docker port-forward` is unnecessary: reach the service at `localhost:<port>` on the Docker host.

### `network_mode: none`

The target has no networking at all. The helper has nowhere to connect. Reconnect the target to a bridge network (`docker network connect bridge <target>`) and retry.

### `network_mode: container:<other>`

Targets that share another container's netns work, but the helper attaches to whichever network the *outer* container is on. Make sure the outer container exposes the remote port on a reachable interface (not loopback-only).

### Macvlan / ipvlan

Macvlan and ipvlan networks often do not permit the Docker host itself to reach containers attached to them (this is a well-known Linux kernel limitation: the host cannot speak to macvlan endpoints on the same physical interface). The helper typically *is* reachable because it is placed on the same macvlan network, but the host's `-p` publish entry ends up bound to a virtual interface the host kernel can't route back to. If the published port works from the LAN but not from the Docker host itself, this is why.

**Workaround:** add a secondary plain `bridge` network to the target (`docker network connect bridge <target>`) and let `docker-port-forward` pick that one (it prefers user-defined networks; add `--address 0.0.0.0` if you need LAN reach).

### Swarm overlay networks

`docker service`-managed tasks live on overlay networks that are not directly addressable from outside a Swarm node in the way `docker run`-style containers are. The helper can be created but the target name/IP it resolves will not route the way you expect. For Swarm services, use Swarm's own port publishing:

```bash
docker service update --publish-add published=8080,target=80 <service>
```

## Unsupported connection targets

### Loopback-only listeners in the target

A process inside the target that binds only to `127.0.0.1` or `::1` cannot be reached by the helper, because the helper attaches to a network (not the target's netns) and therefore talks to the target's *network* interface. This is the most common surprise.

- Auto-detection (`docker port-forward <target>` with no ports) silently skips loopback listeners.
- Passing a loopback-bound remote port explicitly will create the helper successfully but connections will hang or fail because the target-side port is not listening on the container network.

**Fix:** rebind the service inside the container to `0.0.0.0` (or the container's network IP). For example, Postgres:

```conf
listen_addresses = '*'
```

### IPv6-only services inside the target

The helper's socat command currently uses `TCP:<ipv4>:<port>`. A service listening only on IPv6 inside the container cannot be reached. Most images listen on IPv4 by default; if yours does not, rebind it.

## UDP forwarding

UDP is supported via the Docker-style `/udp` suffix on port specs (for example, `53:53/udp`). The helper runs one additional `socat -T <timeout> UDP-LISTEN:<r>,fork,reuseaddr UDP:<target>:<r>` process per distinct UDP remote port, alongside any TCP socats the same helper hosts. UDP is forwarded end-to-end with a few caveats you should be aware of:

- **No connection tracking.** UDP is connectionless; socat approximates a "session" per client source address and keeps a child process alive for each until either end falls idle for the `--udp-timeout` duration (default `60s`). Protocols that expect a long-lived server-initiated stream (for example, multicast RTP or some VPNs) may see packets dropped when a child times out and a new one is started.
- **Idle-timeout sizing.** The default `--udp-timeout 60s` matches the DNS-style "quick request/reply" pattern. For longer-lived UDP workloads (SIP, QUIC, game servers) increase the timeout or set a very large value. Too-small timeouts cause socat to drop state mid-session.
- **SCTP and other non-TCP/UDP protocols are not supported.** Specs like `53:53/sctp` are rejected up front.

### SCTP or other non-TCP/UDP protocols

Not supported. Only `tcp` and `udp` are accepted as protocol suffixes.

## Unsupported host configurations

### Remote Docker daemon

The plugin's preflight check and host-side semantics assume the Docker daemon and the process running `docker port-forward` share a kernel. When `DOCKER_HOST` points at a remote daemon:

- Host-port preflight checks the **local** machine's port availability, while `-p` binds on the **daemon's** host.
- The helper will publish `-p 127.0.0.1:<port>:<remote>` on the remote machine — useless for the client.

Run `docker port-forward` on the same machine as the daemon, or SSH-tunnel the remote port yourself.

### Rootless Docker

Rootless Docker generally works, but the daemon runs as an unprivileged user. That user cannot bind to TCP ports below 1024 on the host, so `-p 80:80` will fail in the daemon before the plugin gets a clear error. Use a non-privileged host port (`-p 8080:80`) or follow the rootless docs to grant `CAP_NET_BIND_SERVICE` to `rootlesskit`.

### Docker Desktop on macOS / Windows

Docker Desktop works, but two caveats apply:

- Published ports transit via VPNKit / WSL-interop. Extremely high throughput or low-latency traffic may be affected.
- Host-port preflight uses `net.Listen` from the plugin process, which is accurate because Docker Desktop binds the host port via the LinuxVM's port-forwarding proxy.

### Privileged host ports

Publishing a port below 1024 requires the daemon's user to be privileged. Docker's default installation runs as root, so this usually works — but the preflight check runs in the plugin process (your shell user) and can't bind such ports itself. The preflight is lenient only for IPv6 availability, **not** for permission errors, so you'll see:

```text
host port 127.0.0.1:80 is not available: listen tcp 127.0.0.1:80: bind: permission denied
```

Use a non-privileged host port (`--address 0.0.0.0 80:80` still requires daemon root, but `8080:80` works universally).

## Auto-detection caveats

- The probe reads `/proc/net/{tcp,tcp6,udp,udp6}`; exotic targets that hide their listeners from `/proc` (e.g., some distroless or seccomp-restricted builds) will report an empty list.
- The probe filters out loopback-only listeners (`127.0.0.0/8`, `::1`) because the forwarder cannot reach them — see [above](#loopback-only-listeners-in-the-target).
- UDP "listeners" are sockets in `/proc/net/udp*` with state `07` (bound, unconnected). Connected UDP sockets (state `01`, typically short-lived clients) are ignored.
- If the target listens on a privileged port (e.g., 80) and you rely on auto-detection, the forwarder will try to publish `80:80` and the preflight check will refuse on any unprivileged host. Pass an explicit spec like `8080:80` instead.

## Operational limits

- **Idempotency is per-pair-overlap, not per-request:** if a running helper for the same target already covers any `(local, remote)` in your request, the command exits `0` without adding the rest. Stop the existing helper first if you need a different combination.
- **One helper per distinct remote port:** socat runs once per unique remote, with `fork` handling concurrent connections. A huge connection volume may still saturate a single process.
- **Helper is unsupervised:** a dying helper (OOM, network loss) is not restarted. Use `docker port-forward cleanup --name <name>` and re-run, or wrap with your own supervisor.
- **No TLS termination:** traffic is proxied raw. If the target serves TLS, clients should connect using its TLS settings; the helper does no re-encoding.

## Reporting new limitations

If you hit a network configuration that should work but does not, open an issue with the output of `docker inspect <target>` (scrubbed of secrets) and `docker network ls`.
