package portforward

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dokku/docker-port-forward/internal"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Options mirrors the arguments and flags of `docker port-forward`.
// Zero values fall back to the CLI flag defaults.
type Options struct {
	// Target is the TARGET argument: container/<id-or-name>,
	// service/<name>, or a bare name.
	Target string
	// Ports are the port spec arguments, in `docker run -p` form:
	// [[ADDRESS:]LOCAL_PORT:]REMOTE_PORT[/udp]. ADDRESS is an IP literal
	// (IPv6 in brackets) or "localhost" and binds that port only there;
	// ADDRESS::REMOTE_PORT auto-allocates the local port. When empty,
	// listening ports are detected in the target.
	Ports []string
	// Addresses mirrors --address: the addresses to bind ports whose spec
	// has no ADDRESS. Defaults to ["localhost"].
	Addresses []string
	// Detach mirrors --detach. When true, Forward returns once the helper
	// is running and the helper keeps running until removed with Cleanup.
	// When false, Forward blocks until ctx is canceled and then removes the
	// helper.
	Detach bool
	// RestartPolicy mirrors --restart, which takes the same values as
	// `docker container create --restart`: "no", "always",
	// "unless-stopped", "on-failure" or "on-failure:N". When empty it
	// defaults to RestartUnlessStopped for detached helpers and RestartNo
	// for attached ones. Attached helpers are auto-removed, which Docker
	// doesn't allow together with a restart policy, so any value other than
	// "" or "no" without Detach is an error.
	RestartPolicy string
	// LogDriver mirrors --log-driver: the helper container's logging
	// driver. Empty uses the daemon default.
	LogDriver string
	// LogOpts mirrors --log-opt: options for LogDriver. Not allowed with
	// the "none" driver.
	LogOpts map[string]string
	// RunningTimeout mirrors --container-running-timeout. Defaults to
	// DefaultRunningTimeout.
	RunningTimeout time.Duration
	// EnvFiles mirrors --env-file. Accepted for parity with the CLI;
	// currently unused, as in the CLI.
	EnvFiles []string
	// Files mirrors --file.
	Files []string
	// HelperImage mirrors --helper-image. Defaults to DefaultHelperImage.
	HelperImage string
	// Labels mirrors --label.
	Labels map[string]string
	// Name mirrors --name. Auto-generated when empty.
	Name string
	// Profiles mirrors --profile. Accepted for parity with the CLI;
	// currently unused, as in the CLI.
	Profiles []string
	// ProjectDirectory mirrors --project-directory. Accepted for parity
	// with the CLI; currently unused, as in the CLI.
	ProjectDirectory string
	// ProjectName mirrors --project-name.
	ProjectName string
	// Pull mirrors --pull. Defaults to PullMissing.
	Pull string
	// SkipPreflight mirrors --skip-preflight: don't check that host ports
	// are free before creating the helper. Needed for ports below 1024 when
	// not running as root; a conflict then surfaces as Docker's publish
	// error.
	SkipPreflight bool
	// UDPTimeout mirrors --udp-timeout. Defaults to DefaultUDPTimeout.
	UDPTimeout time.Duration
	// Client is the Docker client to use. When nil, a client is created
	// from the environment (DOCKER_HOST etc.) and closed before returning.
	// A caller-supplied client is never closed.
	Client client.APIClient
	// Logger receives progress messages. May be nil.
	Logger Logger
}

// Port is one forwarded port.
type Port struct {
	Local    int
	Remote   int
	Protocol string // "tcp" or "udp"
	// Addresses are the host addresses Local is bound on.
	Addresses []string
}

// Result describes the helper container serving the forward. In attached
// mode Forward returns after ctx is canceled, by which point this helper
// has already been removed.
type Result struct {
	HelperID   string
	HelperName string
	Ports      []Port
	// Existing is true when an already-running helper covered the request
	// and nothing new was created.
	Existing bool
}

// Forward is the equivalent of `docker port-forward`.
func Forward(ctx context.Context, opts Options) (Result, error) {
	if opts.Pull == "" {
		opts.Pull = PullMissing
	}
	if opts.HelperImage == "" {
		opts.HelperImage = DefaultHelperImage
	}
	if len(opts.Addresses) == 0 {
		opts.Addresses = []string{"localhost"}
	}
	if opts.RunningTimeout <= 0 {
		opts.RunningTimeout = DefaultRunningTimeout
	}
	if opts.UDPTimeout <= 0 {
		opts.UDPTimeout = DefaultUDPTimeout
	}
	logger := loggerOrNop(opts.Logger)

	validPullPolicies := []string{PullAlways, PullMissing, PullNever}
	if !slices.Contains(validPullPolicies, opts.Pull) {
		return Result{}, fmt.Errorf("invalid --pull value %q: must be one of: always, missing, never", opts.Pull)
	}

	restartPolicy, err := internal.ParseRestartPolicy(opts.RestartPolicy, opts.Detach)
	if err != nil {
		return Result{}, err
	}

	if err := internal.ValidateLogConfig(opts.LogDriver, opts.LogOpts); err != nil {
		return Result{}, err
	}

	parsedTarget, err := internal.ParseTarget(opts.Target)
	if err != nil {
		return Result{}, err
	}

	// Parse explicit port specs up-front so static argument errors are raised
	// before we touch Docker or the filesystem. Auto-detection (no specs)
	// happens later, after the target is resolved.
	var pairs []internal.PortPair
	if len(opts.Ports) > 0 {
		pairs, err = internal.ParsePortSpecs(opts.Ports)
		if err != nil {
			return Result{}, err
		}
	}

	projectName := resolveProjectName(opts, parsedTarget)

	cli, release, err := dockerClient(opts.Client)
	if err != nil {
		return Result{}, err
	}
	defer release()

	target, err := internal.ResolveTarget(ctx, internal.ResolveTargetInput{
		Client:      cli,
		Target:      parsedTarget,
		ProjectName: projectName,
	})
	if err != nil {
		return Result{}, err
	}

	if len(pairs) == 0 {
		logger.Info(fmt.Sprintf("No ports specified; probing %s for listening ports", target.ContainerName))
		detected, err := internal.ProbeListeners(ctx, cli, target.ContainerID, opts.HelperImage, opts.Pull, logger)
		if err != nil {
			return Result{}, fmt.Errorf("error probing target for listening ports: %v", err)
		}
		if len(detected) == 0 {
			return Result{}, errors.New("no non-loopback listening ports detected in target container; pass explicit [LOCAL:]REMOTE[/udp] specs")
		}
		for _, l := range detected {
			pairs = append(pairs, internal.PortPair{LocalPort: l.Port, RemotePort: l.Port, Protocol: l.Protocol})
		}
		logger.Info(fmt.Sprintf("Detected listening ports: %s", formatDetectedListeners(detected)))
	}

	result, err := internal.StartForward(ctx, internal.ForwardInput{
		Client:         cli,
		Target:         target,
		Pairs:          pairs,
		Addresses:      opts.Addresses,
		HelperImage:    opts.HelperImage,
		PullPolicy:     opts.Pull,
		RunningTimeout: opts.RunningTimeout,
		Detach:         opts.Detach,
		RestartPolicy:  restartPolicy,
		LogConfig:      container.LogConfig{Type: opts.LogDriver, Config: opts.LogOpts},
		Name:           opts.Name,
		ExtraLabels:    opts.Labels,
		SkipPreflight:  opts.SkipPreflight,
		UDPTimeout:     opts.UDPTimeout,
		Logger:         logger,
	})
	if err != nil {
		return Result{}, err
	}

	ports := make([]Port, 0, len(result.Pairs))
	for _, p := range result.Pairs {
		ports = append(ports, Port{
			Local:     p.LocalPort,
			Remote:    p.RemotePort,
			Protocol:  string(internal.NormalizeProtocol(p.Protocol)),
			Addresses: internal.PairAddresses(p, opts.Addresses),
		})
	}
	return Result{
		HelperID:   result.HelperID,
		HelperName: result.HelperName,
		Ports:      ports,
		Existing:   result.Existing,
	}, nil
}

// resolveProjectName determines the compose project name to use for service
// resolution. It honors ProjectName when set; otherwise, for service/ or
// bare-name targets, it attempts to auto-detect a compose file in the cwd.
func resolveProjectName(opts Options, target internal.ParsedTarget) string {
	if opts.ProjectName != "" {
		return opts.ProjectName
	}

	if len(opts.Files) > 0 {
		return filepath.Base(filepath.Dir(opts.Files[0]))
	}

	needsCompose := target.Type == internal.TargetTypeService || target.Type == internal.TargetTypeAuto
	if !needsCompose {
		return ""
	}

	composeFile, err := internal.ComposeFile()
	if err != nil {
		return ""
	}
	return filepath.Base(filepath.Dir(composeFile))
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
