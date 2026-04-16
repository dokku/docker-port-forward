package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/dokku/docker-port-forward/internal"
	"github.com/josegonzalez/cli-skeleton/command"
	"github.com/posener/complete"
	flag "github.com/spf13/pflag"
)

// PortForwardCommand implements `docker port-forward`.
type PortForwardCommand struct {
	command.Meta

	addresses        []string
	detach           bool
	envFiles         []string
	extraLabels      []string
	files            []string
	helperImage      string
	name             string
	profiles         []string
	projectDirectory string
	projectName      string
	pull             string
	runningTimeout   time.Duration
	udpTimeout       time.Duration
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
		"Forward localhost:8080 to port 80 on a container":  fmt.Sprintf("%s %s my-container 8080:80", appName, c.Name()),
		"Use the same port locally and remotely":            fmt.Sprintf("%s %s my-container 5000", appName, c.Name()),
		"Forward an auto-allocated local port":              fmt.Sprintf("%s %s my-container :5000", appName, c.Name()),
		"Forward multiple ports at once":                    fmt.Sprintf("%s %s my-container 8080:80 5432:5432", appName, c.Name()),
		"Auto-detect listening ports":                       fmt.Sprintf("%s %s my-container", appName, c.Name()),
		"Run in the background":                             fmt.Sprintf("%s %s --detach --name mydb my-container 5432:5432", appName, c.Name()),
		"Forward to an explicit container by id":            fmt.Sprintf("%s %s container/abc123 8080:80", appName, c.Name()),
		"Forward to a Compose service":                      fmt.Sprintf("%s %s service/web 8080:80", appName, c.Name()),
		"Bind all interfaces":                               fmt.Sprintf("%s %s --address 0.0.0.0 my-container 8080:80", appName, c.Name()),
		"Add extra labels to the helper":                    fmt.Sprintf("%s %s --label team=backend --label env=dev my-container 8080:80", appName, c.Name()),
		"Forward a UDP port":                                fmt.Sprintf("%s %s my-container 53:53/udp", appName, c.Name()),
		"Mix TCP and UDP in one command":                    fmt.Sprintf("%s %s my-container 8080:80 53:53/udp", appName, c.Name()),
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
			Description: "zero or more port specs in [LOCAL_PORT:]REMOTE_PORT form; omit to auto-detect",
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
	f.StringSliceVar(&c.addresses, "address", []string{"localhost"}, "addresses to listen on (comma-separated); may be repeated")
	f.BoolVarP(&c.detach, "detach", "d", false, "run the helper container in the background and return immediately")
	f.DurationVar(&c.runningTimeout, "container-running-timeout", time.Minute, "how long to wait for the helper container to be running")
	f.StringSliceVar(&c.envFiles, "env-file", []string{}, "one or more paths to environment files (for compose interpolation)")
	f.StringSliceVar(&c.extraLabels, "label", []string{}, "extra labels to add to the helper container, in key=value form; may be repeated")
	f.StringSliceVarP(&c.files, "file", "f", []string{}, "one or more paths to Compose files (for service resolution)")
	f.StringVar(&c.helperImage, "helper-image", internal.DefaultHelperImage, "image used for the sidecar helper container")
	f.StringVar(&c.name, "name", "", "name to assign to the helper container; auto-generated when omitted")
	f.StringSliceVar(&c.profiles, "profile", []string{}, "one or more compose profiles to enable")
	f.StringVar(&c.projectDirectory, "project-directory", "", "the path to the compose project directory")
	f.StringVarP(&c.projectName, "project-name", "p", "", "the compose project name")
	f.StringVar(&c.pull, "pull", internal.PullMissing, "pull policy for the helper image (always, missing, never)")
	f.DurationVar(&c.udpTimeout, "udp-timeout", internal.DefaultUDPTimeout, "idle timeout applied to each UDP forward (socat -T)")
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
			"--name":                      complete.PredictAnything,
			"--profile":                   complete.PredictAnything,
			"--project-directory":         complete.PredictDirs("*"),
			"--project-name":              complete.PredictAnything,
			"--pull":                      complete.PredictSet("always", "missing", "never"),
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

	validPullPolicies := []string{internal.PullAlways, internal.PullMissing, internal.PullNever}
	if !slices.Contains(validPullPolicies, c.pull) {
		c.Ui.Error(fmt.Sprintf("invalid --pull value %q: must be one of: always, missing, never", c.pull))
		return 1
	}

	extraLabels, err := parseLabelFlags(c.extraLabels)
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}

	parsedTarget, err := internal.ParseTarget(rest[0])
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}

	// Parse explicit port specs up-front so static argument errors are raised
	// before we touch Docker or the filesystem. Auto-detection (no specs)
	// happens later, after the target is resolved.
	var explicitPairs []internal.PortPair
	if len(rest) >= 2 {
		explicitPairs, err = internal.ParsePortSpecs(rest[1:])
		if err != nil {
			c.Ui.Error(err.Error())
			return 1
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	projectName, err := c.resolveProjectName(ctx, parsedTarget)
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}

	client, err := internal.NewDockerClient()
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}
	defer client.Close()

	target, err := internal.ResolveTarget(ctx, internal.ResolveTargetInput{
		Client:      client,
		Target:      parsedTarget,
		ProjectName: projectName,
	})
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}

	logger, ok := c.Ui.(*command.ZerologUi)
	if !ok {
		c.Ui.Error("UI is not a ZerologUi")
		return 1
	}

	pairs := explicitPairs
	if len(pairs) == 0 {
		logger.Info(fmt.Sprintf("No ports specified; probing %s for listening ports", target.ContainerName))
		detected, err := internal.ProbeListeners(ctx, client, target.ContainerID, c.helperImage, c.pull, logger)
		if err != nil {
			c.Ui.Error(fmt.Sprintf("error probing target for listening ports: %v", err))
			return 1
		}
		if len(detected) == 0 {
			c.Ui.Error("no non-loopback listening ports detected in target container; pass explicit [LOCAL:]REMOTE[/udp] specs")
			return 1
		}
		for _, l := range detected {
			pairs = append(pairs, internal.PortPair{LocalPort: l.Port, RemotePort: l.Port, Protocol: l.Protocol})
		}
		logger.Info(fmt.Sprintf("Detected listening ports: %s", formatDetectedListeners(detected)))
	}

	result, err := internal.StartForward(ctx, internal.ForwardInput{
		Client:         client,
		Target:         target,
		Pairs:          pairs,
		Addresses:      c.addresses,
		HelperImage:    c.helperImage,
		PullPolicy:     c.pull,
		RunningTimeout: c.runningTimeout,
		Detach:         c.detach,
		Name:           c.name,
		ExtraLabels:    extraLabels,
		UDPTimeout:     c.udpTimeout,
		Logger:         logger,
	})
	if err != nil {
		c.Ui.Error(err.Error())
		return 1
	}
	if result.Existing {
		// Idempotent no-op: user asked for something already running.
		return 0
	}
	return 0
}

// resolveProjectName determines the compose project name to use for service
// resolution. It honors the explicit flag when set; otherwise, for service/
// or bare-name targets, it attempts to auto-detect a compose file in the cwd.
func (c *PortForwardCommand) resolveProjectName(ctx context.Context, target internal.ParsedTarget) (string, error) {
	needsCompose := target.Type == internal.TargetTypeService || target.Type == internal.TargetTypeAuto
	if c.projectName != "" {
		return c.projectName, nil
	}

	if len(c.files) > 0 {
		return filepath.Base(filepath.Dir(c.files[0])), nil
	}

	if !needsCompose {
		return "", nil
	}

	composeFile, err := internal.ComposeFile()
	if err != nil {
		return "", nil
	}
	c.files = []string{composeFile}
	return filepath.Base(filepath.Dir(composeFile)), nil
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

func formatDetectedListeners(listeners []internal.Listener) string {
	parts := make([]string, 0, len(listeners))
	for _, l := range listeners {
		proto := internal.NormalizeProtocol(l.Protocol)
		if proto == internal.ProtocolTCP {
			parts = append(parts, fmt.Sprintf("%d", l.Port))
		} else {
			parts = append(parts, fmt.Sprintf("%d/%s", l.Port, proto))
		}
	}
	return strings.Join(parts, ", ")
}
