// Package portforward forwards local ports to running Docker containers or
// Compose services. Its API mirrors the `docker port-forward` and
// `docker port-forward cleanup` commands.
package portforward

import (
	"github.com/dokku/docker-port-forward/internal"
	"github.com/moby/moby/client"
)

// Default values, matching the CLI flag defaults.
const (
	// DefaultHelperImage ("alpine/socat") is the default value of
	// Options.HelperImage.
	DefaultHelperImage = internal.DefaultHelperImage
	// DefaultRunningTimeout (1m) is the default value of
	// Options.RunningTimeout.
	DefaultRunningTimeout = internal.DefaultRunningTimeout
	// DefaultUDPTimeout (60s) is the default value of Options.UDPTimeout.
	DefaultUDPTimeout = internal.DefaultUDPTimeout
)

// Pull policies accepted by Options.Pull.
const (
	PullAlways  = internal.PullAlways  // "always"
	PullMissing = internal.PullMissing // "missing"
	PullNever   = internal.PullNever   // "never"
)

// Restart policies accepted by Options.RestartPolicy. "on-failure" may
// also be given as "on-failure:N" to cap retries at N, matching
// `docker container create --restart`.
const (
	RestartNo            = internal.RestartNo            // "no"
	RestartAlways        = internal.RestartAlways        // "always"
	RestartUnlessStopped = internal.RestartUnlessStopped // "unless-stopped"
	RestartOnFailure     = internal.RestartOnFailure     // "on-failure"
)

// Logger receives progress messages. A nil Logger discards them.
type Logger interface {
	Info(message string)
	Warn(message string)
	Error(message string)
}

type nopLogger struct{}

func (nopLogger) Info(string)  {}
func (nopLogger) Warn(string)  {}
func (nopLogger) Error(string) {}

func loggerOrNop(l Logger) Logger {
	if l == nil {
		return nopLogger{}
	}
	return l
}

// dockerClient returns the Docker client to use and a function that releases
// it. A caller-supplied client is wrapped and never closed; otherwise a client
// is created from the environment and closed by the release function.
func dockerClient(c client.APIClient) (internal.DockerClientInterface, func(), error) {
	if c != nil {
		return internal.WrapDockerClient(c), func() {}, nil
	}
	cli, err := internal.NewDockerClient()
	if err != nil {
		return nil, nil, err
	}
	return cli, func() { _ = cli.Close() }, nil
}
