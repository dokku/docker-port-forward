package portforward

import (
	"context"

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
	// Stale mirrors --stale: only act on helpers that can no longer reach
	// their target (see Helper.Stale).
	Stale bool
	// Client is the Docker client to use. When nil, a client is created
	// from the environment and closed before returning. A caller-supplied
	// client is never closed.
	Client client.APIClient
	// Logger receives progress messages. May be nil.
	Logger Logger
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
// is removed. With Stale set, only stale helpers are removed. With DryRun
// set, matching helpers are returned but not removed. Per-helper removal
// failures are logged and reflected in Removed, not returned as an error.
func Cleanup(ctx context.Context, opts CleanupOptions) (CleanupResult, error) {
	logger := loggerOrNop(opts.Logger)

	cli, release, err := dockerClient(opts.Client)
	if err != nil {
		return CleanupResult{}, err
	}
	defer release()

	helpers, err := findHelpers(ctx, cli, opts.Name, opts.Target, opts.Stale)
	if err != nil {
		return CleanupResult{}, err
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
