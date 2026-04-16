# Documentation

Complete documentation for docker-port-forward, a Docker CLI plugin that forwards local ports to running containers or Compose services with a `kubectl port-forward`-style interface.

## Getting Started

- [Getting Started](getting-started.md) -- why docker-port-forward, installation, and your first forward

## Reference

- [Command Reference](command-reference.md) -- all CLI flags and arguments

## Guides

- [Target Resolution](target-resolution.md) -- how `container/`, `service/`, and bare-name targets are resolved
- [Compose Integration](compose-integration.md) -- resolving Compose services with `-f`/`-p`/profiles
- [Helper Image](helper-image.md) -- the sidecar container used to proxy connections
- [Limitations](limitations.md) -- network modes, target configurations, and host setups that are not supported
