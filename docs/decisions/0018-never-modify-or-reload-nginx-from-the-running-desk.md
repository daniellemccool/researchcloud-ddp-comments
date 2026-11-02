---
status: accepted
date: "2026-10-30"
category: Deploy
applies_to:
    - cmd/desk/**/*.go
    - internal/docker/**/*.go
    - tests/box/nginx_test.go
priority: default
checks:
    - desc: the running desk has no code path to nginx
      grep: 'nginx|systemctl|/etc/nginx'
      in: ['cmd/desk/**/*.go', 'internal/**/*.go']
      except: ['internal/arch/**', '**/*_test.go']
      expect: absent
---
# Never modify or reload nginx from the running desk

## Decision

After the init script has written its two location files, nothing of ours touches nginx: the running desk has no code path to render an nginx file or reload nginx, and no collector lifecycle operation changes what nginx serves.

## Guidance

- Per-user routing is the desk's own work (the gated listener and the screen relay); `tests/box/nginx_test.go` hashes the nginx tree and reads the reload count before and after start, stop, retire and repair.
- `os/exec` is allowed only in `internal/docker` (depguard); the absence of `nginx`, `systemctl` and `/etc/nginx` under `cmd/desk` and `internal/` is this record's check.
- A need to change nginx after launch is a change to the init script and a documented `init.sh apply`, not a desk feature.

## Why

A container that can reload the host's nginx needs a privilege hack; keeping nginx static is what lets the desk run with no host privilege beyond the Docker socket.
