package portforward

import (
	"context"
	"fmt"
	"strings"

	"github.com/dokku/docker-port-forward/internal"
	"github.com/moby/moby/client"
)

// CleanupOptions mirrors the flags of `docker port-forward cleanup`.
type CleanupOptions struct {
	// DryRun mirrors --dry-run.
	DryRun bool
	// Name mirrors --name.
	Name string
	// Target mirrors --target.
	Target string
	// Client is the Docker client to use. When nil, a client is created
	// from the environment and closed before returning. A caller-supplied
	// client is never closed.
	Client client.APIClient
	// Logger receives progress messages. May be nil.
	Logger Logger
}

// Helper is a helper container found by Cleanup.
type Helper struct {
	ID     string
	Name   string
	Target string // target container id
	Ports  string // encoded as LOCAL:REMOTE[/udp],...
}

// CleanupResult describes what Cleanup found and removed.
type CleanupResult struct {
	Helpers []Helper
	// Removed is the number of helpers removed. Always 0 when DryRun is set.
	Removed int
}

// Cleanup is the equivalent of `docker port-forward cleanup`. It
// force-removes matching helper containers, killing them if they are
// running (detached or attached). With no Name or Target set, every helper
// is removed. With DryRun set, matching helpers are returned but not removed.
// Per-helper removal failures are logged and reflected in Removed, not
// returned as an error.
func Cleanup(ctx context.Context, opts CleanupOptions) (CleanupResult, error) {
	logger := loggerOrNop(opts.Logger)

	cli, release, err := dockerClient(opts.Client)
	if err != nil {
		return CleanupResult{}, err
	}
	defer release()

	// Resolve filters. Name short-circuits to a single helper lookup;
	// Target is used to narrow the label query.
	var helpers []Helper
	if opts.Name != "" {
		info, err := cli.ContainerInspect(ctx, opts.Name)
		if err != nil {
			return CleanupResult{}, fmt.Errorf("error looking up helper %q: %v", opts.Name, err)
		}
		if info.Config == nil || info.Config.Labels[internal.LabelPortForward] != "true" {
			return CleanupResult{}, fmt.Errorf("container %q is not a port-forward helper", opts.Name)
		}
		helpers = append(helpers, Helper{
			ID:     info.ID,
			Name:   strings.TrimPrefix(info.Name, "/"),
			Target: info.Config.Labels[internal.LabelTarget],
			Ports:  info.Config.Labels[internal.LabelPorts],
		})
	} else {
		targetID := ""
		if opts.Target != "" {
			info, err := cli.ContainerInspect(ctx, opts.Target)
			if err != nil {
				return CleanupResult{}, fmt.Errorf("error resolving target %q: %v", opts.Target, err)
			}
			targetID = info.ID
		}
		list, err := internal.ListHelpers(ctx, cli, targetID)
		if err != nil {
			return CleanupResult{}, fmt.Errorf("error listing helper containers: %v", err)
		}
		for _, h := range list {
			name := ""
			if len(h.Names) > 0 {
				name = strings.TrimPrefix(h.Names[0], "/")
			}
			helpers = append(helpers, Helper{
				ID:     h.ID,
				Name:   name,
				Target: h.Labels[internal.LabelTarget],
				Ports:  h.Labels[internal.LabelPorts],
			})
		}
	}

	result := CleanupResult{Helpers: helpers}
	if len(helpers) == 0 || opts.DryRun {
		return result, nil
	}

	ids := make([]string, 0, len(helpers))
	for _, h := range helpers {
		ids = append(ids, h.ID)
	}
	result.Removed = internal.RemoveHelpers(ctx, cli, ids, logger)
	return result, nil
}
