# Compose Integration

`docker-port-forward` understands Compose-specific flags so you can forward to a service by name, even when multiple replicas, profiles, or compose files are in play. These flags only matter when your target is a Compose service (`service/<name>` or a bare name that falls through to service lookup).

## Auto-detection

When you run `docker port-forward` in a directory that contains `docker-compose.yaml` or `docker-compose.yml`, the plugin will auto-detect the file and derive a project name from the directory. You generally do not need `-f` or `-p` in this case.

```bash
cd my-project
docker port-forward web 8080:80
```

## Explicit compose files

Use `-f/--file` to point at one or more specific compose files. Later files layer on top of earlier ones, mirroring `docker compose -f a.yml -f b.yml`.

```bash
docker port-forward -f docker-compose.yml -f docker-compose.override.yml service/web 8080:80
```

## Project name

Compose containers are identified by the `com.docker.compose.project` label. Set this explicitly with `-p/--project-name`:

```bash
docker port-forward -p my-project service/web 8080:80
```

If you omit `-p`:

- When `-f` is given, the project name defaults to the base name of the directory containing the first compose file.
- When `-f` is not given, it defaults to the base name of an auto-detected compose file's directory.

## Profiles

Pass `--profile` one or more times, or as a comma-separated list, to match services that are only active under specific profiles. This only affects *parsing* of the compose file (for completeness with `docker compose`); runtime service lookup is always done by label matching, so an already-running service will be found regardless of profile.

```bash
docker port-forward --profile dev service/api 3000:3000
```

## Environment files

`--env-file` paths are forwarded to the compose loader for variable interpolation. This is relevant when your compose file references `${VAR}` values that are defined in env files.

```bash
docker port-forward --env-file .env.local service/db 5432:5432
```

## Multiple replicas

When a service has more than one running container, `docker-port-forward` selects the replica with the lowest `com.docker.compose.container-number` label. If you need a specific replica, target it by container name or ID:

```bash
docker port-forward container/my-project-web-2 8080:80
```

## See also

- [Target Resolution](target-resolution.md) for the full lookup order.
- [Command Reference](command-reference.md) for every Compose-related flag.
