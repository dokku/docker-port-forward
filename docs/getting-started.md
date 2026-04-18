# Getting Started

`docker-port-forward` is a Docker CLI plugin that forwards one or more local ports to a running container, with a command-line interface modeled on `kubectl port-forward`. It is especially useful for reaching services that bind to loopback addresses inside a container (for example, a database that listens on `127.0.0.1:5432` without publishing the port).

## Why docker-port-forward?

Docker already supports port publishing with `docker run -p`, but only at container creation time. If a container is already running and a port you need was never published, your only options are typically to restart the container with new port mappings or to `docker exec` into it. Neither is ideal.

`docker-port-forward` behaves like `kubectl port-forward`: you specify one or more `[LOCAL_PORT:]REMOTE_PORT` pairs and a target, and a foreground process proxies TCP connections from your host to the container. It works even when the remote port is only bound to the container's loopback interface, because the forwarder runs inside the container's network namespace.

## Installation

### One-line install

```bash
curl -fsSL https://raw.githubusercontent.com/dokku/docker-port-forward/main/install.sh | sh
```

### Build from source

```bash
make install
```

This builds the plugin binary for your platform and copies it to `~/.docker/cli-plugins/docker-pf`.

### Pre-built binaries

Download a binary from [GitHub Releases](https://github.com/dokku/docker-port-forward/releases) and place it in `~/.docker/cli-plugins/`. The binary must be named `docker-pf` and be executable.

```bash
install -m 0755 docker-port-forward-linux-amd64 ~/.docker/cli-plugins/docker-pf
```

Once installed, `docker pf --help` should print the usage text.

> **Direct invocation:** The binary can also be run directly as `docker-port-forward port-forward TARGET ...` without installing it as a plugin.

## Your first forward

Start any container that listens on a port internally:

```bash
docker run -d --name demo nginx
```

`nginx` in this image listens on port 80 inside the container, but the port is not published to the host. Forward it:

```bash
docker pf demo 8080:80
```

Leave that process running. In another terminal:

```bash
curl http://127.0.0.1:8080
```

You will see the default nginx welcome page. Press `Ctrl-C` in the first terminal to stop the forward -- the sidecar helper container is cleaned up automatically.

## What to read next

- The [Command Reference](command-reference.md) lists every flag and argument.
- [Target Resolution](target-resolution.md) explains how container names, container IDs, and Compose service names are disambiguated.
- [Compose Integration](compose-integration.md) covers forwarding to Compose services by name, including profiles and multi-file setups.
- [Helper Image](helper-image.md) describes the sidecar container that powers the forward, and how to override it in restricted environments.
