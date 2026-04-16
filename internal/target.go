package internal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerClient "github.com/docker/docker/client"
)

// Compose label keys used for service/container resolution.
const (
	ComposeProjectLabel      = "com.docker.compose.project"
	ComposeServiceLabel      = "com.docker.compose.service"
	ComposeContainerNumLabel = "com.docker.compose.container-number"
)

// TargetType represents the parsed target form.
type TargetType string

// Target types.
const (
	// TargetTypeContainer denotes an explicit "container/<id-or-name>" target.
	TargetTypeContainer TargetType = "container"
	// TargetTypeService denotes an explicit "service/<name>" target.
	TargetTypeService TargetType = "service"
	// TargetTypeAuto denotes a bare name: try container first, then service.
	TargetTypeAuto TargetType = ""
)

// ParsedTarget is the result of parsing the first positional argument.
type ParsedTarget struct {
	Type TargetType
	Name string
}

// ParseTarget parses a kubectl-style target argument:
//
//	"container/<id-or-name>" -> explicit container
//	"service/<name>"         -> explicit compose service
//	"<name>"                 -> bare, resolved as container then service
func ParseTarget(s string) (ParsedTarget, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ParsedTarget{}, errors.New("target is required")
	}
	if name, ok := strings.CutPrefix(s, "container/"); ok {
		if name == "" {
			return ParsedTarget{}, errors.New("container name is empty")
		}
		return ParsedTarget{Type: TargetTypeContainer, Name: name}, nil
	}
	if name, ok := strings.CutPrefix(s, "service/"); ok {
		if name == "" {
			return ParsedTarget{}, errors.New("service name is empty")
		}
		return ParsedTarget{Type: TargetTypeService, Name: name}, nil
	}
	return ParsedTarget{Type: TargetTypeAuto, Name: s}, nil
}

// ResolveTargetInput bundles inputs for ResolveTarget.
type ResolveTargetInput struct {
	Client      DockerClientInterface
	Target      ParsedTarget
	ProjectName string
}

// ResolvedTarget is the outcome of target resolution.
type ResolvedTarget struct {
	ContainerID   string
	ContainerName string
}

// ResolveTarget returns a running container matching the parsed target.
//
// For TargetTypeAuto it first tries to find a container with that name or ID;
// on miss it falls back to a compose service lookup if ProjectName is set.
func ResolveTarget(ctx context.Context, in ResolveTargetInput) (ResolvedTarget, error) {
	switch in.Target.Type {
	case TargetTypeContainer:
		return resolveContainer(ctx, in.Client, in.Target.Name)
	case TargetTypeService:
		if in.ProjectName == "" {
			return ResolvedTarget{}, errors.New("service/ target requires a compose project (set --project-name or -f)")
		}
		return resolveService(ctx, in.Client, in.ProjectName, in.Target.Name)
	case TargetTypeAuto:
		if t, err := resolveContainer(ctx, in.Client, in.Target.Name); err == nil {
			return t, nil
		} else if !isNotFound(err) {
			return ResolvedTarget{}, err
		}
		if in.ProjectName == "" {
			return ResolvedTarget{}, fmt.Errorf("no container named %q found; no compose project is available for service lookup", in.Target.Name)
		}
		return resolveService(ctx, in.Client, in.ProjectName, in.Target.Name)
	}
	return ResolvedTarget{}, fmt.Errorf("invalid target type %q", in.Target.Type)
}

func resolveContainer(ctx context.Context, cli DockerClientInterface, nameOrID string) (ResolvedTarget, error) {
	info, err := cli.ContainerInspect(ctx, nameOrID)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if info.State == nil || !info.State.Running {
		return ResolvedTarget{}, fmt.Errorf("container %q is not running", nameOrID)
	}
	return ResolvedTarget{
		ContainerID:   info.ID,
		ContainerName: strings.TrimPrefix(info.Name, "/"),
	}, nil
}

func resolveService(ctx context.Context, cli DockerClientInterface, projectName, serviceName string) (ResolvedTarget, error) {
	f := filters.NewArgs()
	f.Add("label", fmt.Sprintf("%s=%s", ComposeProjectLabel, projectName))
	f.Add("label", fmt.Sprintf("%s=%s", ComposeServiceLabel, serviceName))

	list, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return ResolvedTarget{}, fmt.Errorf("error listing containers: %v", err)
	}

	running := make([]container.Summary, 0, len(list))
	for _, c := range list {
		if c.State == "running" {
			running = append(running, c)
		}
	}
	if len(running) == 0 {
		return ResolvedTarget{}, fmt.Errorf("no running container found for service %q in project %q", serviceName, projectName)
	}

	sort.SliceStable(running, func(i, j int) bool {
		return containerNumber(running[i].Labels) < containerNumber(running[j].Labels)
	})
	chosen := running[0]
	name := ""
	if len(chosen.Names) > 0 {
		name = strings.TrimPrefix(chosen.Names[0], "/")
	}
	return ResolvedTarget{ContainerID: chosen.ID, ContainerName: name}, nil
}

func containerNumber(labels map[string]string) int {
	n, err := strconv.Atoi(labels[ComposeContainerNumLabel])
	if err != nil || n <= 0 {
		return 1<<31 - 1
	}
	return n
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if dockerClient.IsErrNotFound(err) {
		return true
	}
	// Fallback: some errors don't implement the docker error interface cleanly.
	return strings.Contains(strings.ToLower(err.Error()), "no such container")
}
