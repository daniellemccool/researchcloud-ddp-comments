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
	composeOneoffLabel  = "com.docker.compose.oneoff"
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

// findOne returns the name of the single service container carrying the
// compose project label and the given service label. Containers labelled
// oneoff (a `docker compose run` side container) are ignored before counting.
func findOne(ctx context.Context, d docker.Client, service string) (string, error) {
	list, err := d.ContainerList(ctx, map[string]string{
		composeProjectLabel: composeProject,
		composeServiceLabel: service,
	})
	if err != nil {
		return "", err
	}
	var found []docker.Container
	for _, ctr := range list {
		if ctr.Labels[composeOneoffLabel] == "True" {
			continue
		}
		found = append(found, ctr)
	}
	if len(found) != 1 {
		return "", errContainersNotFound
	}
	return found[0].Name, nil
}

// lookupLocked finds the still-empty container names and stores them on the
// client; a name the caller set is kept. The caller must hold c.mu. A failed
// lookup stores nothing, so the next call retries.
func (c *Client) lookupLocked(ctx context.Context) error {
	if c.Backend == "" {
		backend, err := findOne(ctx, c.Docker, "4cat-backend")
		if err != nil {
			return err
		}
		c.Backend = backend
	}
	if c.DB == "" {
		db, err := findOne(ctx, c.Docker, "4cat-db")
		if err != nil {
			return err
		}
		c.DB = db
	}
	return nil
}

// backend returns the backend container name, looking it up lazily when
// unset. The empty-check, the lookup and the read all happen under c.mu, so
// concurrent provisioners cannot race on the names; a failed lookup is
// returned without anything else being done.
func (c *Client) backend(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Backend == "" {
		if err := c.lookupLocked(ctx); err != nil {
			return "", err
		}
	}
	return c.Backend, nil
}

// db returns the database container name, looking it up lazily when unset,
// under the same lock as backend.
func (c *Client) db(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.DB == "" {
		if err := c.lookupLocked(ctx); err != nil {
			return "", err
		}
	}
	return c.DB, nil
}
