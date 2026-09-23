package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/dokku/docker-port-forward/portforward"
	"github.com/josegonzalez/cli-skeleton/command"
	"github.com/posener/complete"
	flag "github.com/spf13/pflag"
)

// PortForwardCleanupCommand removes leftover helper sidecar containers.
type PortForwardCleanupCommand struct {
	command.Meta

	dryRun bool
	name   string
	stale  bool
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
		"Remove all stale helpers":                     fmt.Sprintf("%s %s", appName, c.Name()),
		"Preview what would be removed":                fmt.Sprintf("%s %s --dry-run", appName, c.Name()),
		"Remove only helpers for a specific target":    fmt.Sprintf("%s %s --target abc123", appName, c.Name()),
		"Remove a specific helper by name":             fmt.Sprintf("%s %s --name port-forward-mydb-a9c2", appName, c.Name()),
		"Remove helpers that can't reach their target": fmt.Sprintf("%s %s --stale", appName, c.Name()),
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
	f.BoolVar(&c.stale, "stale", false, "only act on helpers that can no longer reach their target")
	f.StringVar(&c.target, "target", "", "only act on helpers for the given target container id or name")
	return f
}

func (c *PortForwardCleanupCommand) AutocompleteFlags() complete.Flags {
	return command.MergeAutocompleteFlags(
		c.Meta.AutocompleteFlags(command.FlagSetClient),
		complete.Flags{
			"--dry-run": complete.PredictNothing,
			"--name":    complete.PredictAnything,
			"--stale":   complete.PredictNothing,
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

	logger, ok := c.Ui.(*command.ZerologUi)
	if !ok {
		c.Ui.Error("UI is not a ZerologUi")
		return 1
	}

	result, err := portforward.Cleanup(context.Background(), portforward.CleanupOptions{
		DryRun: c.dryRun,
		Name:   c.name,
		Stale:  c.stale,
		Target: c.target,
		Logger: logger,
	})
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}

	if len(result.Helpers) == 0 {
		c.Ui.Info("No helper containers found.")
		return 0
	}

	for _, h := range result.Helpers {
		c.Ui.Info(formatHelperRow(h, false))
	}

	if c.dryRun {
		c.Ui.Info(fmt.Sprintf("--dry-run: would remove %d helper container(s)", len(result.Helpers)))
		return 0
	}

	c.Ui.Info(fmt.Sprintf("Removed %d of %d helper container(s).", result.Removed, len(result.Helpers)))
	if result.Removed != len(result.Helpers) {
		return 1
	}
	return 0
}

// formatHelperRow renders one helper for `cleanup` and `list` output. The
// detailed form adds the network and address the helper dials. A stale
// helper always ends with its stale reason.
func formatHelperRow(h portforward.Helper, detailed bool) string {
	row := fmt.Sprintf("%s  name=%s target=%s ports=%s",
		truncateID(h.ID), h.Name, truncateID(h.Target), h.Ports)
	if detailed {
		row += fmt.Sprintf(" bindings=%s network=%s address=%s", h.Bindings, h.TargetNetwork, h.TargetAddress)
	}
	if h.Stale {
		row += fmt.Sprintf(" stale=%q", h.StaleReason)
	}
	return row
}

func truncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
