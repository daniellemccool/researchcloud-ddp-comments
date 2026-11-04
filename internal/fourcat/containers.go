package fourcat

import (
	"context"
	"errors"

	"github.com/daniellemccool/researchcloud-ddp-comments/internal/docker"
)

// errContainersNotFound is returned when the compose labels do not match
// exactly one 4CAT backend or database container.
var errContainersNotFound = errors.New("4CAT containers not found")

// The compose labels of the bundled 4CAT containers.
const (
	composeProjectLabel = "com.docker.compose.project"
	composeServiceLabel = "com.docker.compose.service"
	composeProject      = "desk"
)

// FindContainers returns the names of the 4CAT backend and database
// containers, found by their compose labels. Each service must match exactly
// one container; otherwise errContainersNotFound is returned.
func FindContainers(ctx context.Context, d docker.Client) (backend, db string, err error) {
	backend, err = findOne(ctx, d, "4cat-backend")
	if err != nil {
		return "", "", err
	}
	db, err = findOne(ctx, d, "4cat-db")
	if err != nil {
		return "", "", err
	}
	return backend, db, nil
}

// findOne returns the name of the single container carrying the compose
// project label and the given service label.
func findOne(ctx context.Context, d docker.Client, service string) (string, error) {
	list, err := d.ContainerList(ctx, map[string]string{
		composeProjectLabel: composeProject,
		composeServiceLabel: service,
	})
	if err != nil {
		return "", err
	}
	if len(list) != 1 {
		return "", errContainersNotFound
	}
	return list[0].Name, nil
}

// lookup finds the 4CAT containers and stores them on the client.
func (c *Client) lookup(ctx context.Context) error {
	backend, db, err := FindContainers(ctx, c.Docker)
	if err != nil {
		return err
	}
	c.Backend, c.DB = backend, db
	return nil
}

// backend returns the backend container name, looking it up lazily when
// unset. A failed lookup is returned without anything else being done.
func (c *Client) backend(ctx context.Context) (string, error) {
	if c.Backend == "" {
		if err := c.lookup(ctx); err != nil {
			return "", err
		}
	}
	return c.Backend, nil
}

// db returns the database container name, looking it up lazily when unset.
func (c *Client) db(ctx context.Context) (string, error) {
	if c.DB == "" {
		if err := c.lookup(ctx); err != nil {
			return "", err
		}
	}
	return c.DB, nil
}
