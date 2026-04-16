package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
)

// ComposeFile returns the absolute path to a compose file discovered in the
// current working directory. It prefers `docker-compose.yaml` over
// `docker-compose.yml`. Returns an error if no file is found.
func ComposeFile() (string, error) {
	for _, name := range []string{"docker-compose.yaml", "docker-compose.yml"} {
		if _, err := os.Stat(name); err == nil {
			abs, err := filepath.Abs(name)
			if err != nil {
				return "", fmt.Errorf("error expanding path: %v", err)
			}
			return abs, nil
		}
	}
	return "", errors.New("no compose file found")
}

// ComposeProject loads a compose project from the given file(s).
func ComposeProject(ctx context.Context, projectName string, filenames []string, profiles []string, envFiles []string) (*types.Project, error) {
	opts := []cli.ProjectOptionsFn{
		cli.WithOsEnv,
		cli.WithEnvFiles(envFiles...),
		cli.WithDotEnv,
		cli.WithDefaultProfiles(profiles...),
		cli.WithName(projectName),
	}

	options, err := cli.NewProjectOptions(filenames, opts...)
	if err != nil {
		return nil, fmt.Errorf("error creating project options: %v", err)
	}

	project, err := options.LoadProject(ctx)
	if err != nil {
		return nil, fmt.Errorf("error loading project: %v", err)
	}

	return project, nil
}
