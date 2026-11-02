---
status: accepted
date: "2026-10-30"
category: Deploy
applies_to:
    - deploy/init.sh
    - deploy/compose.yml
priority: default
---
# Keep init.sh to validation, files and pulls; start nothing

## Decision

On the platform-invoked initialisation path, run once as root before compose up, the init script validates every parameter, writes `.env`, `desk.json`, the volume layout and the two nginx location files, pulls the pinned images, fetches and checks the template artifact, runs `nginx -t` and reloads nginx once, and starts no container and builds no image.

## Guidance

- Values the compose file interpolates come only from `.env`; `src_ansible.env` is written after the script and feeds container environments only; shellcheck and shfmt run on the script.
- The operator modes `update` and `apply` are separate functions of the same script, entered only by an explicit argument; `update` is the one place `compose up -d` appears, and `apply` only asks the running desk to render; the initialisation path calls neither.
- A validation failure prints one line per problem and exits non-zero before touching the box, so the provisioning log carries the reason; `/etc/nginx/app-location-conf.d/authentication.conf` and everything under `/etc/rsc/` are never written.

## Why

The platform restarts Docker right after the script and runs compose itself; a container started early is killed and an image built on the box blows the provisioning timeout.
