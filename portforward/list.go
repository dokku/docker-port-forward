package portforward

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dokku/docker-port-forward/internal"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// ErrNotHelper is returned (wrapped) by List and Cleanup when Name refers to
// an existing container that isn't a port-forward helper. Check it with
// errors.Is.
var ErrNotHelper = errors.New("not a port-forward helper")

// ListOptions mirrors the flags of `docker port-forward list`.
type ListOptions struct {
	// Name mirrors --name.
	Name string
	// Target mirrors --target.
	Target string
	// Stale mirrors --stale: only return helpers that can no longer reach
	// their target (see Helper.Stale).
	Stale bool
	// Client is the Docker client to use. When nil, a client is created
	// from the environment and closed before returning. A caller-supplied
	// client is never closed.
	Client client.APIClient
	// Logger receives progress messages. May be nil.
	Logger Logger
}

// Helper is a helper container found by List or Cleanup.
type Helper struct {
	ID     string
	Name   string
	Target string // target container id
	Ports  string // encoded as LOCAL:REMOTE[/udp],...
	// Bindings lists every published binding as
	// ADDRESS:LOCAL:REMOTE[/udp],..., with IPv6 addresses bracketed.
	Bindings string
	// TargetName is the target's container name when the helper was
	// created.
	TargetName string
	// TargetNetwork is the network the helper shares with the target.
	TargetNetwork string
	// TargetAddress is what the helper dials: the target's container name
	// on a user-defined network, or its IP on the default bridge.
	TargetAddress string
	// Stale is true when the helper can no longer reach its target: the
	// target container is gone, has left TargetNetwork, or (for helpers
	// that dial an IP) is running with a different IP. A stopped target is
	// not stale. Helpers created before these labels existed are never
	// reported stale.
	Stale bool
	// StaleReason explains why Stale is true.
	StaleReason string
}

// List is the equivalent of `docker port-forward list`. It returns helper
// containers, optionally narrowed to one helper by Name, to one target, or
// to stale helpers only.
func List(ctx context.Context, opts ListOptions) ([]Helper, error) {
	cli, release, err := dockerClient(opts.Client)
	if err != nil {
		return nil, err
	}
	defer release()

	return findHelpers(ctx, cli, opts.Name, opts.Target, opts.Stale)
}

// findHelpers looks up helper containers shared by List and Cleanup. name
// short-circuits to a single helper lookup; target narrows the label query.
func findHelpers(ctx context.Context, cli internal.DockerClientInterface, name, target string, staleOnly bool) ([]Helper, error) {
	var summaries []container.Summary
	if name != "" {
		info, err := cli.ContainerInspect(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("error looking up helper %q: %w", name, err)
		}
		if info.Config == nil || info.Config.Labels[internal.LabelPortForward] != "true" {
			return nil, fmt.Errorf("container %q is %w", name, ErrNotHelper)
		}
		state := ""
		if info.State != nil {
			state = string(info.State.Status)
		}
		summaries = append(summaries, container.Summary{
			ID:     info.ID,
			Names:  []string{info.Name},
			Labels: info.Config.Labels,
			State:  container.ContainerState(state),
		})
	} else {
		targetID := ""
		if target != "" {
			info, err := cli.ContainerInspect(ctx, target)
			if err != nil {
				return nil, fmt.Errorf("error resolving target %q: %v", target, err)
			}
			targetID = info.ID
		}
		list, err := internal.ListHelpers(ctx, cli, targetID)
		if err != nil {
			return nil, fmt.Errorf("error listing helper containers: %v", err)
		}
		summaries = list
	}

	helpers := make([]Helper, 0, len(summaries))
	for _, s := range summaries {
		stale, reason, err := internal.CheckHelper(ctx, cli, s)
		if err != nil {
			return nil, err
		}
		if staleOnly && !stale {
			continue
		}
		helperName := ""
		if len(s.Names) > 0 {
			helperName = strings.TrimPrefix(s.Names[0], "/")
		}
		helpers = append(helpers, Helper{
			ID:            s.ID,
			Name:          helperName,
			Target:        s.Labels[internal.LabelTarget],
			Ports:         s.Labels[internal.LabelPorts],
			Bindings:      s.Labels[internal.LabelBindings],
			TargetName:    s.Labels[internal.LabelTargetName],
			TargetNetwork: s.Labels[internal.LabelTargetNetwork],
			TargetAddress: s.Labels[internal.LabelTargetAddress],
			Stale:         stale,
			StaleReason:   reason,
		})
	}
	return helpers, nil
}
