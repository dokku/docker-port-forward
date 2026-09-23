# Library Usage

The `github.com/dokku/docker-port-forward/portforward` package exposes the same functionality as the CLI to Go programs. Its API mirrors the commands:

| Command | Function | Options |
| ------- | -------- | ------- |
| `docker port-forward` | `portforward.Forward` | `portforward.Options` |
| `docker port-forward list` | `portforward.List` | `portforward.ListOptions` |
| `docker port-forward cleanup` | `portforward.Cleanup` | `portforward.CleanupOptions` |

Each option field maps to the flag or argument of the same name in the [Command Reference](command-reference.md), and zero values fall back to the CLI defaults. Two fields have no flag equivalent:

- `Client` - a `github.com/moby/moby/client.APIClient` to use. When nil, a client is created from the environment (`DOCKER_HOST` etc.) and closed before the function returns. A client you pass in is never closed.
- `Logger` - receives the progress messages the CLI prints. When nil, messages are discarded.

## Installation

```bash
go get github.com/dokku/docker-port-forward
```

## Forward in the background

With `Detach: true`, `Forward` returns once the helper container is running. The helper keeps running (restart policy `unless-stopped` by default) until it is removed with `Cleanup`.

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/dokku/docker-port-forward/portforward"
)

func main() {
    result, err := portforward.Forward(context.Background(), portforward.Options{
        Target: "my-db",
        Ports:  []string{"5432:5432"},
        Detach: true,
        Name:   "mydb-forward",
    })
    if err != nil {
        log.Fatal(err)
    }

    for _, p := range result.Ports {
        fmt.Printf("forwarding %d -> %d/%s via %s\n", p.Local, p.Remote, p.Protocol, result.HelperName)
    }
}
```

`result.Existing` is true when a running helper already covered the request and nothing new was created, matching the CLI's [idempotency](command-reference.md#idempotency) behavior. A covering helper that can no longer reach its target is replaced instead, and `Existing` is false.

## Per-port addresses and logging

`Ports` takes the same `docker run -p` style specs as the CLI, so each port can be bound on its own address. Specs without an address are bound on every entry in `Addresses`. `LogDriver` and `LogOpts` set the helper's logging, like `--log-driver` and `--log-opt`. `SkipPreflight`, like `--skip-preflight`, skips the host-port check so ports below 1024 can be forwarded when the program isn't running as root.

```go
result, err := portforward.Forward(ctx, portforward.Options{
    Target:    "my-app",
    Ports:     []string{"127.0.0.1:8080:80", "0.0.0.0:5432:5432", "[::1]::9000"},
    Detach:    true,
    LogDriver: "json-file",
    LogOpts:   map[string]string{"max-size": "10m"},
})
if err != nil {
    log.Fatal(err)
}
for _, p := range result.Ports {
    fmt.Printf("%v:%d -> %d/%s\n", p.Addresses, p.Local, p.Remote, p.Protocol)
}
```

## Finding stale helpers

`List` returns helper containers along with the network and address each one dials. `Stale` is true when a helper can no longer reach its target, for example after a target on the default `bridge` network restarted with a new IP. See [`port-forward list`](command-reference.md#port-forward-list) for the exact rules.

```go
helpers, err := portforward.List(ctx, portforward.ListOptions{Stale: true})
if err != nil {
    log.Fatal(err)
}
for _, h := range helpers {
    fmt.Printf("%s dials %s on %s: %s\n", h.Name, h.TargetAddress, h.TargetNetwork, h.StaleReason)
}
```

To fix them, call `Forward` again with the same options, which replaces a stale helper that covers the requested ports, or remove them with `Cleanup` and `CleanupOptions{Stale: true}`.

## Forward until canceled

Without `Detach`, `Forward` blocks until the context is canceled, then stops and removes the helper, like pressing Ctrl-C in the CLI.

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

go func() {
    _, err := portforward.Forward(ctx, portforward.Options{
        Target: "service/web",
        Ports:  []string{":80"},
    })
    if err != nil {
        log.Print(err)
    }
}()

// ... use the forward, then call cancel() to tear it down.
```

## Cleanup

`Cleanup` removes helper containers, killing them first if they are running. With no `Name` or `Target`, every helper is removed. With `DryRun`, matching helpers are returned but not removed.

```go
result, err := portforward.Cleanup(context.Background(), portforward.CleanupOptions{
    Name: "mydb-forward",
})
if err != nil {
    log.Fatal(err)
}
fmt.Printf("removed %d of %d helpers\n", result.Removed, len(result.Helpers))
```

Removal failures for individual helpers are passed to the `Logger` and reflected in `Removed`; they are not returned as an error.

## Using your own Docker client

```go
cli, err := client.NewClientWithOpts(client.WithHost("unix:///var/run/docker.sock"), client.WithAPIVersionNegotiation())
if err != nil {
    log.Fatal(err)
}
defer cli.Close()

_, err = portforward.Forward(ctx, portforward.Options{
    Target: "my-container",
    Ports:  []string{"8080:80"},
    Detach: true,
    Client: cli,
})
```

Here `client` is `github.com/moby/moby/client`.
