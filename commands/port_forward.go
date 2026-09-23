package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dokku/docker-port-forward/portforward"
	"github.com/josegonzalez/cli-skeleton/command"
	"github.com/posener/complete"
	flag "github.com/spf13/pflag"
)

// PortForwardCommand implements `docker port-forward`.
type PortForwardCommand struct {
	command.Meta

	addresses           []string
	detach              bool
	envFiles            []string
	extraLabels         []string
	files               []string
	helperImage         string
	logDriver           string
	logOpts             []string
	name                string
	profiles            []string
	projectDirectory    string
	projectName         string
	pull                string
	restart             string
	skipPreflight       bool
	tcpHalfCloseTimeout time.Duration
	runningTimeout      time.Duration
	udpTimeout          time.Duration
}

func (c *PortForwardCommand) Name() string {
	return "port-forward"
}

func (c *PortForwardCommand) Synopsis() string {
	return "Forward one or more local ports to a container"
}

func (c *PortForwardCommand) Help() string {
	return command.CommandHelp(c)
}

func (c *PortForwardCommand) Examples() map[string]string {
	appName := os.Getenv("CLI_APP_NAME")
	return map[string]string{
		"Forward localhost:8080 to port 80 on a container": fmt.Sprintf("%s %s my-container 8080:80", appName, c.Name()),
		"Use the same port locally and remotely":           fmt.Sprintf("%s %s my-container 5000", appName, c.Name()),
		"Forward an auto-allocated local port":             fmt.Sprintf("%s %s my-container :5000", appName, c.Name()),
		"Forward multiple ports at once":                   fmt.Sprintf("%s %s my-container 8080:80 5432:5432", appName, c.Name()),
		"Auto-detect listening ports":                      fmt.Sprintf("%s %s my-container", appName, c.Name()),
		"Run in the background":                            fmt.Sprintf("%s %s --detach --name mydb my-container 5432:5432", appName, c.Name()),
		"Keep a detached helper from restarting":           fmt.Sprintf("%s %s --detach --restart no my-container 5432:5432", appName, c.Name()),
		"Forward to an explicit container by id":           fmt.Sprintf("%s %s container/abc123 8080:80", appName, c.Name()),
		"Forward to a Compose service":                     fmt.Sprintf("%s %s service/web 8080:80", appName, c.Name()),
		"Bind all interfaces":                              fmt.Sprintf("%s %s --address 0.0.0.0 my-container 8080:80", appName, c.Name()),
		"Add extra labels to the helper":                   fmt.Sprintf("%s %s --label team=backend --label env=dev my-container 8080:80", appName, c.Name()),
		"Bind each port on its own address":                fmt.Sprintf("%s %s my-container 127.0.0.1:8080:80 0.0.0.0:5432:5432", appName, c.Name()),
		"Bind all interfaces, IPv4 and IPv6":               fmt.Sprintf("%s %s --address '*' my-container 8080:80", appName, c.Name()),
		"Bind one port on all interfaces":                  fmt.Sprintf("%s %s my-container :8080:80", appName, c.Name()),
		"Wait for replies after a client half-closes":      fmt.Sprintf("%s %s --detach --tcp-half-close-timeout 100000000s my-container 8080:80", appName, c.Name()),
		"Forward a privileged port":                        fmt.Sprintf("%s %s --skip-preflight my-container 80:80", appName, c.Name()),
		"Configure the helper's logging":                   fmt.Sprintf("%s %s --detach --log-driver json-file --log-opt max-size=10m my-container 8080:80", appName, c.Name()),
		"Forward a UDP port":                               fmt.Sprintf("%s %s my-container 53:53/udp", appName, c.Name()),
		"Mix TCP and UDP in one command":                   fmt.Sprintf("%s %s my-container 8080:80 53:53/udp", appName, c.Name()),
	}
}

func (c *PortForwardCommand) Arguments() []command.Argument {
	return []command.Argument{
		{
			Name:        "target",
			Description: "the target to forward to: container/<id-or-name>, service/<name>, or a bare name",
			Optional:    false,
			Type:        command.ArgumentString,
		},
		{
			Name:        "ports",
			Description: "zero or more port specs in [[ADDRESS:]LOCAL_PORT:]REMOTE_PORT[/udp] form; omit to auto-detect",
			Optional:    true,
			Type:        command.ArgumentList,
		},
	}
}

func (c *PortForwardCommand) AutocompleteArgs() complete.Predictor {
	return complete.PredictNothing
}

func (c *PortForwardCommand) ParsedArguments(args []string) (map[string]command.Argument, error) {
	return command.ParseArguments(args, c.Arguments())
}

func (c *PortForwardCommand) FlagSet() *flag.FlagSet {
	f := c.Meta.FlagSet(c.Name(), command.FlagSetClient)
	f.StringSliceVar(&c.addresses, "address", []string{"localhost"}, "addresses to listen on (comma-separated); may be repeated; \"*\" binds all interfaces (IPv4 and IPv6)")
	f.BoolVarP(&c.detach, "detach", "d", false, "run the helper container in the background and return immediately")
	f.DurationVar(&c.runningTimeout, "container-running-timeout", portforward.DefaultRunningTimeout, "how long to wait for the helper container to be running")
	f.StringSliceVar(&c.envFiles, "env-file", []string{}, "one or more paths to environment files (for compose interpolation)")
	f.StringSliceVar(&c.extraLabels, "label", []string{}, "extra labels to add to the helper container, in key=value form; may be repeated")
	f.StringSliceVarP(&c.files, "file", "f", []string{}, "one or more paths to Compose files (for service resolution)")
	f.StringVar(&c.helperImage, "helper-image", portforward.DefaultHelperImage, "image used for the sidecar helper container")
	f.StringVar(&c.logDriver, "log-driver", "", "Logging driver for the container")
	f.StringArrayVar(&c.logOpts, "log-opt", []string{}, "Log driver options")
	f.StringVar(&c.name, "name", "", "name to assign to the helper container; auto-generated when omitted")
	f.StringSliceVar(&c.profiles, "profile", []string{}, "one or more compose profiles to enable")
	f.StringVar(&c.projectDirectory, "project-directory", "", "the path to the compose project directory")
	f.StringVarP(&c.projectName, "project-name", "p", "", "the compose project name")
	f.StringVar(&c.pull, "pull", portforward.PullMissing, "pull policy for the helper image (always, missing, never)")
	f.BoolVar(&c.skipPreflight, "skip-preflight", false, "skip checking that host ports are free before creating the helper; needed for ports below 1024 when not running as root")
	f.StringVar(&c.restart, "restart", "", "Restart policy to apply when a container exits (default \"unless-stopped\" with --detach, \"no\" otherwise)")
	f.DurationVar(&c.tcpHalfCloseTimeout, "tcp-half-close-timeout", 0, "how long each TCP forward waits for the other side after one side closes its write half (socat -t); 0 keeps socat's 0.5s default")
	f.DurationVar(&c.udpTimeout, "udp-timeout", portforward.DefaultUDPTimeout, "idle timeout applied to each UDP forward (socat -T)")
	return f
}

func (c *PortForwardCommand) AutocompleteFlags() complete.Flags {
	return command.MergeAutocompleteFlags(
		c.Meta.AutocompleteFlags(command.FlagSetClient),
		complete.Flags{
			"--address":                   complete.PredictAnything,
			"--container-running-timeout": complete.PredictAnything,
			"--detach":                    complete.PredictNothing,
			"--env-file":                  complete.PredictFiles("*"),
			"--file":                      complete.PredictFiles("*"),
			"--helper-image":              complete.PredictAnything,
			"--label":                     complete.PredictAnything,
			"--log-driver":                complete.PredictAnything,
			"--log-opt":                   complete.PredictAnything,
			"--name":                      complete.PredictAnything,
			"--profile":                   complete.PredictAnything,
			"--project-directory":         complete.PredictDirs("*"),
			"--project-name":              complete.PredictAnything,
			"--pull":                      complete.PredictSet("always", "missing", "never"),
			"--restart":                   complete.PredictSet("no", "always", "unless-stopped", "on-failure"),
			"--skip-preflight":            complete.PredictNothing,
			"--tcp-half-close-timeout":    complete.PredictAnything,
			"--udp-timeout":               complete.PredictAnything,
		},
	)
}

func (c *PortForwardCommand) Run(args []string) int {
	flags := c.FlagSet()
	flags.Usage = func() { c.Ui.Output(c.Help()) }
	if err := flags.Parse(args); err != nil {
		c.Ui.Error(err.Error())
		c.Ui.Error(command.CommandErrorText(c))
		return 1
	}

	rest := flags.Args()
	if len(rest) < 1 {
		c.Ui.Error("usage: port-forward TARGET [[LOCAL_PORT:]REMOTE_PORT ...]")
		c.Ui.Error(command.CommandErrorText(c))
		return 1
	}

	extraLabels, err := parseLabelFlags(c.extraLabels)
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}

	logger, ok := c.Ui.(*command.ZerologUi)
	if !ok {
		c.Ui.Error("UI is not a ZerologUi")
		return 1
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	_, err = portforward.Forward(ctx, portforward.Options{
		Target:              rest[0],
		Ports:               rest[1:],
		Addresses:           c.addresses,
		Detach:              c.detach,
		RestartPolicy:       c.restart,
		RunningTimeout:      c.runningTimeout,
		EnvFiles:            c.envFiles,
		Files:               c.files,
		HelperImage:         c.helperImage,
		Labels:              extraLabels,
		LogDriver:           c.logDriver,
		LogOpts:             parseLogOptFlags(c.logOpts),
		Name:                c.name,
		Profiles:            c.profiles,
		ProjectDirectory:    c.projectDirectory,
		ProjectName:         c.projectName,
		Pull:                c.pull,
		SkipPreflight:       c.skipPreflight,
		TCPHalfCloseTimeout: c.tcpHalfCloseTimeout,
		UDPTimeout:          c.udpTimeout,
		Logger:              logger,
	})
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}
	return 0
}

// parseLogOptFlags converts --log-opt key=value flags to a map the way
// `docker container create --log-opt` does: each value is cut on the first
// "=", and an entry without "=" maps to an empty value.
func parseLogOptFlags(in []string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for _, entry := range in {
		k, v, _ := strings.Cut(entry, "=")
		out[k] = v
	}
	return out
}

// parseLabelFlags parses --label key=value flags into a map, returning a clear
// error when a value is malformed.
func parseLabelFlags(in []string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for _, entry := range in {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			return nil, fmt.Errorf("invalid --label value %q: expected key=value", entry)
		}
		out[parts[0]] = parts[1]
	}
	return out, nil
}
