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

// PortForwardListCommand lists helper sidecar containers and whether they
// can still reach their target.
type PortForwardListCommand struct {
	command.Meta

	name   string
	stale  bool
	target string
}

func (c *PortForwardListCommand) Name() string {
	return "port-forward list"
}

func (c *PortForwardListCommand) Synopsis() string {
	return "List port-forward helper containers"
}

func (c *PortForwardListCommand) Help() string {
	return command.CommandHelp(c)
}

func (c *PortForwardListCommand) Examples() map[string]string {
	appName := os.Getenv("CLI_APP_NAME")
	return map[string]string{
		"List all helpers":                           fmt.Sprintf("%s %s", appName, c.Name()),
		"List helpers for a specific target":         fmt.Sprintf("%s %s --target abc123", appName, c.Name()),
		"Show a specific helper by name":             fmt.Sprintf("%s %s --name port-forward-mydb-a9c2", appName, c.Name()),
		"List helpers that can't reach their target": fmt.Sprintf("%s %s --stale", appName, c.Name()),
	}
}

func (c *PortForwardListCommand) Arguments() []command.Argument {
	return []command.Argument{}
}

func (c *PortForwardListCommand) AutocompleteArgs() complete.Predictor {
	return complete.PredictNothing
}

func (c *PortForwardListCommand) ParsedArguments(args []string) (map[string]command.Argument, error) {
	return command.ParseArguments(args, c.Arguments())
}

func (c *PortForwardListCommand) FlagSet() *flag.FlagSet {
	f := c.Meta.FlagSet(c.Name(), command.FlagSetClient)
	f.StringVar(&c.name, "name", "", "only show the helper with this container name")
	f.BoolVar(&c.stale, "stale", false, "only show helpers that can no longer reach their target")
	f.StringVar(&c.target, "target", "", "only show helpers for the given target container id or name")
	return f
}

func (c *PortForwardListCommand) AutocompleteFlags() complete.Flags {
	return command.MergeAutocompleteFlags(
		c.Meta.AutocompleteFlags(command.FlagSetClient),
		complete.Flags{
			"--name":   complete.PredictAnything,
			"--stale":  complete.PredictNothing,
			"--target": complete.PredictAnything,
		},
	)
}

func (c *PortForwardListCommand) Run(args []string) int {
	flags := c.FlagSet()
	flags.Usage = func() { c.Ui.Output(c.Help()) }
	if err := flags.Parse(args); err != nil {
		c.Ui.Error(err.Error())
		c.Ui.Error(command.CommandErrorText(c))
		return 1
	}

	helpers, err := portforward.List(context.Background(), portforward.ListOptions{
		Name:   c.name,
		Stale:  c.stale,
		Target: c.target,
	})
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}

	if len(helpers) == 0 {
		c.Ui.Info("No helper containers found.")
		return 0
	}
	for _, h := range helpers {
		c.Ui.Info(formatHelperRow(h, true))
	}
	return 0
}
