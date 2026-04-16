package commands

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/dokku/docker-port-forward/internal"
	"github.com/josegonzalez/cli-skeleton/command"
	"github.com/posener/complete"
	flag "github.com/spf13/pflag"
)

// PortForwardCleanupCommand removes leftover helper sidecar containers.
type PortForwardCleanupCommand struct {
	command.Meta

	dryRun bool
	name   string
	target string
}

func (c *PortForwardCleanupCommand) Name() string {
	return "port-forward cleanup"
}

func (c *PortForwardCleanupCommand) Synopsis() string {
	return "Remove leftover port-forward helper containers"
}

func (c *PortForwardCleanupCommand) Help() string {
	return command.CommandHelp(c)
}

func (c *PortForwardCleanupCommand) Examples() map[string]string {
	appName := os.Getenv("CLI_APP_NAME")
	return map[string]string{
		"Remove all stale helpers":                   fmt.Sprintf("%s %s", appName, c.Name()),
		"Preview what would be removed":              fmt.Sprintf("%s %s --dry-run", appName, c.Name()),
		"Remove only helpers for a specific target":  fmt.Sprintf("%s %s --target abc123", appName, c.Name()),
		"Remove a specific helper by name":           fmt.Sprintf("%s %s --name port-forward-mydb-a9c2", appName, c.Name()),
	}
}

func (c *PortForwardCleanupCommand) Arguments() []command.Argument {
	return []command.Argument{}
}

func (c *PortForwardCleanupCommand) AutocompleteArgs() complete.Predictor {
	return complete.PredictNothing
}

func (c *PortForwardCleanupCommand) ParsedArguments(args []string) (map[string]command.Argument, error) {
	return command.ParseArguments(args, c.Arguments())
}

func (c *PortForwardCleanupCommand) FlagSet() *flag.FlagSet {
	f := c.Meta.FlagSet(c.Name(), command.FlagSetClient)
	f.BoolVar(&c.dryRun, "dry-run", false, "list helpers that would be removed without removing them")
	f.StringVar(&c.name, "name", "", "only act on the helper with this container name")
	f.StringVar(&c.target, "target", "", "only act on helpers for the given target container id or name")
	return f
}

func (c *PortForwardCleanupCommand) AutocompleteFlags() complete.Flags {
	return command.MergeAutocompleteFlags(
		c.Meta.AutocompleteFlags(command.FlagSetClient),
		complete.Flags{
			"--dry-run": complete.PredictNothing,
			"--name":    complete.PredictAnything,
			"--target":  complete.PredictAnything,
		},
	)
}

func (c *PortForwardCleanupCommand) Run(args []string) int {
	flags := c.FlagSet()
	flags.Usage = func() { c.Ui.Output(c.Help()) }
	if err := flags.Parse(args); err != nil {
		c.Ui.Error(err.Error())
		c.Ui.Error(command.CommandErrorText(c))
		return 1
	}

	ctx := context.Background()

	client, err := internal.NewDockerClient()
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}
	defer client.Close()

	// Resolve filters. --name short-circuits to a single helper lookup;
	// --target is used to narrow the label query.
	var candidates []helperRow
	if c.name != "" {
		info, err := client.ContainerInspect(ctx, c.name)
		if err != nil {
			c.Ui.Error(fmt.Sprintf("error looking up helper %q: %v", c.name, err))
			return 1
		}
		if info.Config == nil || info.Config.Labels[internal.LabelPortForward] != "true" {
			c.Ui.Error(fmt.Sprintf("container %q is not a port-forward helper", c.name))
			return 1
		}
		candidates = append(candidates, helperRow{
			ID:     info.ID,
			Name:   strings.TrimPrefix(info.Name, "/"),
			Target: info.Config.Labels[internal.LabelTarget],
			Ports:  info.Config.Labels[internal.LabelPorts],
		})
	} else {
		targetID := ""
		if c.target != "" {
			info, err := client.ContainerInspect(ctx, c.target)
			if err != nil {
				c.Ui.Error(fmt.Sprintf("error resolving target %q: %v", c.target, err))
				return 1
			}
			targetID = info.ID
		}
		helpers, err := internal.ListHelpers(ctx, client, targetID)
		if err != nil {
			c.Ui.Error(fmt.Sprintf("error listing helper containers: %v", err))
			return 1
		}
		for _, h := range helpers {
			name := ""
			if len(h.Names) > 0 {
				name = strings.TrimPrefix(h.Names[0], "/")
			}
			candidates = append(candidates, helperRow{
				ID:     h.ID,
				Name:   name,
				Target: h.Labels[internal.LabelTarget],
				Ports:  h.Labels[internal.LabelPorts],
			})
		}
	}

	if len(candidates) == 0 {
		c.Ui.Info("No helper containers found.")
		return 0
	}

	for _, row := range candidates {
		c.Ui.Info(fmt.Sprintf("%s  name=%s target=%s ports=%s",
			truncateID(row.ID), row.Name, truncateID(row.Target), row.Ports))
	}

	if c.dryRun {
		c.Ui.Info(fmt.Sprintf("--dry-run: would remove %d helper container(s)", len(candidates)))
		return 0
	}

	logger, ok := c.Ui.(*command.ZerologUi)
	if !ok {
		c.Ui.Error("UI is not a ZerologUi")
		return 1
	}

	ids := make([]string, 0, len(candidates))
	for _, h := range candidates {
		ids = append(ids, h.ID)
	}
	removed := internal.RemoveHelpers(ctx, client, ids, logger)
	c.Ui.Info(fmt.Sprintf("Removed %d of %d helper container(s).", removed, len(candidates)))
	if removed != len(candidates) {
		return 1
	}
	return 0
}

type helperRow struct {
	ID     string
	Name   string
	Target string
	Ports  string
}

func truncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
