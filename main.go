package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dokku/docker-port-forward/commands"

	"github.com/josegonzalez/cli-skeleton/command"
	"github.com/mitchellh/cli"
)

// AppName is the binary / plugin name.
var AppName = "docker-port-forward"

// Version is injected at build time.
var Version string

func main() {
	os.Exit(Run(os.Args[1:]))
}

// Run executes the specified subcommand and returns an exit code.
func Run(args []string) int {
	ctx := context.Background()
	commandMeta := command.SetupRun(ctx, AppName, Version, args)
	commandMeta.Ui = command.HumanZerologUiWithFields(commandMeta.Ui, make(map[string]interface{}, 0))
	// When invoked by `docker pf ...`, Docker CLI prepends the
	// plugin name as argv[1] and sets DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND.
	// Strip "pf" and implicitly route to the "port-forward" subcommand so
	// users type `docker pf TARGET ...` instead of `docker pf port-forward TARGET ...`.
	cliArgs := os.Args[1:]
	if os.Getenv("DOCKER_CLI_PLUGIN_ORIGINAL_CLI_COMMAND") != "" &&
		len(os.Args) > 1 && os.Args[1] == "pf" {
		cliArgs = append([]string{"port-forward"}, os.Args[2:]...)
	}

	c := cli.NewCLI(AppName, Version)
	c.Args = cliArgs
	c.Commands = command.Commands(ctx, commandMeta, Commands)
	c.HiddenCommands = []string{"docker-cli-plugin-metadata"}
	exitCode, err := c.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error executing CLI: %s\n", err.Error())
		return 1
	}

	return exitCode
}

// Commands returns the subcommand factory map for the CLI.
func Commands(ctx context.Context, meta command.Meta) map[string]cli.CommandFactory {
	return map[string]cli.CommandFactory{
		"port-forward": func() (cli.Command, error) {
			return &commands.PortForwardCommand{Meta: meta}, nil
		},
		"port-forward cleanup": func() (cli.Command, error) {
			return &commands.PortForwardCleanupCommand{Meta: meta}, nil
		},
		"docker-cli-plugin-metadata": func() (cli.Command, error) {
			return &commands.DockerCliPluginMetadataCommand{Meta: meta, Version: Version}, nil
		},
		"version": func() (cli.Command, error) {
			return &command.VersionCommand{Meta: meta}, nil
		},
	}
}
