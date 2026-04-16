# docker-port-forward

A Docker CLI plugin that mirrors the `kubectl port-forward` interface. Forward one or more local ports to a running container or a Docker Compose service, without publishing ports or modifying the container.

## Installation

Build and install as a Docker CLI plugin:

```bash
make install
```

Or download a pre-built binary from [GitHub Releases](https://github.com/dokku/docker-port-forward/releases) and place it in `~/.docker/cli-plugins/`. See the [Getting Started](docs/getting-started.md#installation) guide for details.

Once installed, the plugin is available via `docker port-forward`.

## Usage

Forward `localhost:8080` to port `80` on a running container:

```bash
docker port-forward my-container 8080:80
```

Auto-allocate a local port:

```bash
docker port-forward my-container :5000
```

Forward to a Compose service by name (with an auto-detected compose file):

```bash
docker port-forward service/web 8080:80
```

Forward multiple ports at once:

```bash
docker port-forward my-container 8080:80 5432:5432
```

Auto-detect all non-loopback TCP and UDP listeners in a container and forward each on its own port:

```bash
docker port-forward my-container
```

Forward UDP (DNS-style):

```bash
docker port-forward my-dns 5353:53/udp
```

Mix TCP and UDP in one invocation:

```bash
docker port-forward my-app 8080:80 53:53/udp
```

Run in the background and give the helper an explicit name:

```bash
docker port-forward --detach --name mydb my-db 5432:5432
```

Remove any leftover helper containers from crashed or detached sessions:

```bash
docker port-forward cleanup
docker port-forward cleanup --name mydb
```

See the [command reference](docs/command-reference.md) for all flags and options.

## Documentation

- [Getting Started](docs/getting-started.md) -- why docker-port-forward, installation, and your first forward
- [Command Reference](docs/command-reference.md) -- all CLI flags and arguments
- [Target Resolution](docs/target-resolution.md) -- how `container/`, `service/`, and bare-name targets are resolved
- [Compose Integration](docs/compose-integration.md) -- resolving Compose services with `-f`/`-p`/profiles
- [Helper Image](docs/helper-image.md) -- what the sidecar does, why, and how to override `--helper-image`
- [Limitations](docs/limitations.md) -- network modes, target configurations, and host setups that are not supported

## License

[MIT](LICENSE)
