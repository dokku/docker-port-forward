# Target Resolution

The first positional argument to `docker port-forward` identifies the container to forward to. Three forms are accepted, mirroring `kubectl port-forward`'s `pod/<name>`, `service/<name>`, and bare-name conventions.

## `container/<name-or-id>`

An explicit container reference. The plugin runs `docker inspect` against the name or ID and fails if the container does not exist or is not running.

```bash
docker port-forward container/my-app 8080:80
docker port-forward container/abc123def456 8080:80
```

Use this form when you want to be completely unambiguous, or when a container name happens to collide with a Compose service name.

## `service/<name>`

An explicit Compose service reference. The plugin looks up the running container that carries the standard Compose labels:

- `com.docker.compose.project=<project-name>`
- `com.docker.compose.service=<service-name>`

If the service has multiple replicas, the one with the lowest `com.docker.compose.container-number` label is selected deterministically. This matches how `docker compose exec` picks an instance.

```bash
docker port-forward service/web 8080:80
```

The project name is determined from `-p/--project-name`, or -- if absent -- from the directory of the first `-f/--file` argument, or from the directory of an auto-detected `docker-compose.yaml`/`docker-compose.yml` in the current working directory.

## Bare name: `<name>`

Without a prefix, the plugin tries two lookups, in order:

1. Treat the argument as a container name or ID (like `container/<name>`).
2. If no matching container is found and a Compose project can be determined, treat it as a Compose service name (like `service/<name>`).

```bash
# Works whether "web" is a container name or a Compose service name.
docker port-forward web 8080:80
```

This is the most ergonomic form for day-to-day use. If both a container and a Compose service share the same name, the container wins. Use `service/<name>` to force the Compose path.

## When no compose file is available

Bare-name and `service/` targets require a Compose project. If none can be found (no `-f` flag, no compose file in the current directory, no `-p` flag), bare-name lookup will fail with a clear error, and `service/` will refuse to run at all.

## See also

- [Compose Integration](compose-integration.md) for how `-f`, `-p`, `--profile`, and `--env-file` affect service lookup.
- [Command Reference](command-reference.md) for the full flag list.
