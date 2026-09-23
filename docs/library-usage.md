# Library Usage

The `github.com/dokku/docker-port-forward/portforward` package exposes the same functionality as the CLI to Go programs. Its API mirrors the two commands:

| Command | Function | Options |
| ------- | -------- | ------- |
| `docker port-forward` | `portforward.Forward` | `portforward.Options` |
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

`result.Existing` is true when a running helper already covered the request and nothing new was created, matching the CLI's [idempotency](command-reference.md#idempotency) behavior.

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
