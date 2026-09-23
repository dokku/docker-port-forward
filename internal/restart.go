package internal

import (
	"errors"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
)

// ParseRestartPolicy parses a restart policy using the same syntax and error
// messages as `docker container create --restart`: "no", "always",
// "unless-stopped", "on-failure" or "on-failure:N".
//
// An empty policy resolves to unless-stopped for detached helpers and "no"
// for attached ones. Attached helpers are auto-removed, which Docker doesn't
// allow together with a restart policy, so any policy other than "no" is
// rejected when detach is false.
func ParseRestartPolicy(policy string, detach bool) (container.RestartPolicy, error) {
	if policy == "" {
		if detach {
			return container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}, nil
		}
		return container.RestartPolicy{Name: container.RestartPolicyDisabled}, nil
	}

	name, count, ok := strings.Cut(policy, ":")
	if ok && name == "" {
		return container.RestartPolicy{}, errors.New("invalid restart policy format: no policy provided before colon")
	}

	var retryCount int
	if count != "" {
		c, err := strconv.Atoi(count)
		if err != nil {
			return container.RestartPolicy{}, errors.New("invalid restart policy format: maximum retry count must be an integer")
		}
		retryCount = c
	}

	rp := container.RestartPolicy{
		Name:              container.RestartPolicyMode(name),
		MaximumRetryCount: retryCount,
	}
	if err := container.ValidateRestartPolicy(rp); err != nil {
		return container.RestartPolicy{}, err
	}

	if !detach && !rp.IsNone() {
		return container.RestartPolicy{}, errors.New("conflicting options: cannot specify both --restart and an attached (auto-removed) helper; use --detach")
	}
	return rp, nil
}
