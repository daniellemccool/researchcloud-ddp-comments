---
status: accepted
date: "2026-11-05"
category: Provisioning
applies_to:
    - internal/render/spec.go
    - internal/docker/**/*.go
    - internal/config/**/memory*.go
    - internal/arch/pair_test.go
priority: invariant
---

# Cap every pair container's memory and the seats by memory

## Decision

Every container of a collector pair is created with a hard memory limit and no swap (Firefox 3 GiB, proxy 256 MiB), and the number of pairs that may run at once is capped by the box's memory, so one runaway browser is killed by the kernel instead of taking the box down.

## Guidance

- `render.Build` sets `Memory` and `MemorySwap` on both container specs from `config.FirefoxMemory` (3 << 30), `config.ProxyMemory` (256 << 20); `MemorySwap` equals `Memory`, so there is no swap. `internal/docker` passes them to the daemon through `hostConfig`; `internal/arch/pair_test.go` (`TestPairContainersHaveMemoryLimits`) is the enforcement and review rejects a pair container without a limit.
- Seats: `desk serve` reads `/proc/meminfo` and runs at most min(`desk_max_running`, max(1, (MemTotal - `config.MemoryReserve`) / (Firefox + proxy limits))) pairs, with `MemoryReserve` = 4 << 30 kept for the OS, the desk, 4CAT and nginx. `auto` is unchanged on the three workspace variants (2 core/16 GB gives 1, 4 core/16 GB gives 3, 8 core/32 GB gives 7); a lower result is logged and shown on the first line of `desk status`.
- `Reconcile` adopts a complete current-generation pair only when both containers report the spec's `Memory`; otherwise it stops the pair ("limits changed") and the next visit restarts it capped. The owner-label and generation rules stay as they are.
- Raising a limit needs a new sandbox measurement recorded here first; never raise one to make a failing session pass.

## Why

Measured 2026-11-05 on a 16 GB sandbox: Firefox peak 2328.6 MiB over three TikTok comment sessions; proxy peak 20.8 MiB. Without limits, a few browsers on a shared 16 GB workspace can exhaust memory and swap and stall every collector and 4CAT; the limits give headroom over the measured peaks and the seat formula keeps their sum under the box.
