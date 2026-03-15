# Architecture

## Overview

`pve-imds` intercepts HTTP requests from guest VMs to `169.254.169.254` and proxies them to an unprivileged metadata backend, adding VM identity information as request headers. Privilege separation keeps the high-privilege XDP layer minimal and allows the metadata backend to be replaced or extended independently.

```
┌─────────────────────────────────────────────────────────────┐
│ Proxmox Host                                                │
│                                                             │
│  ┌────────────┐   tap100i0   ┌───────────────────────────┐  │
│  │  VM 100    │◄────────────►│  pve-imds (root/CAP_BPF)  │  │
│  │            │              │                           │  │
│  │  GET /meta │              │  XDP: intercept 169.254.. │  │
│  │  →169.254  │              │  gvisor netstack: HTTP    │  │
│  └────────────┘              │  add identity headers     │  │
│                              │         │                 │  │
│                              └─────────┼─────────────────┘  │
│                                        │ Unix socket         │
│                              ┌─────────▼─────────────────┐  │
│                              │  pve-imds-meta (unpriv.)  │  │
│                              │                           │  │
│                              │  serve metadata           │  │
│                              │  sign identity documents  │  │
│                              └───────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

## Packet path

1. Guest sends `GET http://169.254.169.254/...` → tap interface on host.
2. eBPF XDP program attached to the tap interface checks: IPv4, TCP, dst=`169.254.169.254`, dport=80. Matching packets are redirected to an AF_XDP socket via `XSKMAP`; non-matching packets pass through normally.
3. gvisor's userspace netstack receives frames from the AF_XDP socket and presents a standard `net.Listener` interface.
4. An HTTP reverse proxy layer adds `X-PVE-*` identity headers and forwards the request to the metadata backend over a Unix socket.
5. The response is returned through the same path.

The XDP program runs in copy-mode (not zero-copy) for compatibility; zero-copy can be enabled where the driver supports it.

## Interface lifecycle

```
netlink RTM_NEWLINK (tap prefix)
    │
    ▼
parse vmid + net_index from interface name
    │
    ▼
lookup VM identity (cache → /proc + /etc/pve/qemu-server/)
    │
    ▼
create AF_XDP socket
load & attach eBPF program
register HTTP proxy handler
    │
    ▼ (running)
    │
netlink RTM_DELLINK  ──►  detach XDP, close socket, remove handler
```

Interface names follow the Proxmox convention `tap{vmid}i{netindex}`. The daemon may also watch `/etc/pve/qemu-server/*.conf` via inotify to detect config changes (e.g., config digest updates) without relying on netlink alone, since Proxmox does not guarantee a generic pre/post-start hook mechanism.

## VM identity cache

Resolving the full identity tuple `(node, vmid, qemu_pid, qemu_pid_starttime, net_index, config_digest)` involves filesystem and procfs reads. This information is cached in memory per tap interface and refreshed:

- On interface creation (cold start).
- When a request arrives and the config digest has changed (detected lazily from `inotify` or on-demand hash comparison).
- Explicitly on cache invalidation signals.

`qemu_pid_starttime` from `/proc/{pid}/stat` field 22 (in jiffies since boot) is included to prevent PID reuse from causing stale identity hits.

## Repository layout

```
pve-imds/
├── cmd/
│   ├── pve-imds/           # Main daemon binary
│   │   └── main.go
│   └── pve-imds-meta/      # Metadata backend binary (planned)
│       └── main.go
├── internal/
│   ├── cli/                # cobra/viper command setup (root, serve subcommands)
│   ├── config/             # Config structs
│   ├── ifwatch/            # Netlink tap interface watcher
│   ├── vmid/               # Interface name parsing, VM identity resolution, cache
│   ├── xdp/                # eBPF program (xdp_redirect.c), bpf2go bindings, AF_XDP socket management
│   ├── proxy/              # HTTP proxy: header injection, unix socket forwarding
│   ├── meta/               # Metadata backend: handler, identity document signing
│   └── logging/            # slog setup, common fields
├── go.mod
├── go.sum
├── README.md
├── ARCHITECTURE.md
└── CLAUDE.md
```

Each `internal/` package exposes an `fx.Module()` function consumed by the CLI layer, following the pattern in `~/git/repocache`.

## CLI and configuration

Binaries use [spf13/cobra](https://github.com/spf13/cobra) for subcommand structure and [spf13/viper](https://github.com/spf13/viper) for configuration, consistent with `repocache`. [uber/fx](https://github.com/uber-go/fx) wires components together via dependency injection.

Typical invocation:

```sh
pve-imds serve --socket /run/pve-imds-meta.sock --log-level info
```

Configuration is layered: config file < environment variables (`PVE_IMDS_*`) < CLI flags.

## Observability

- **Logging**: structured `slog` throughout, with tap interface name and VMID as common log fields.
- **Metrics**: Prometheus metrics exposed on a configurable HTTP port. Key metrics include:
  - Active tap interfaces under management
  - Requests proxied / errors by interface
  - XDP redirect counts (from eBPF map statistics)
  - Identity cache hit/miss ratio
  - Backend unix socket latency histogram

## Testing strategy

The project targets a testing pyramid:

| Layer | Approach |
|-------|----------|
| **Unit** | Pure Go logic: interface name parsing, identity cache, header injection, config parsing. No kernel or Proxmox required. |
| **Integration** | XDP socket attachment and packet forwarding tested with a `veth` pair and a minimal network namespace. Requires Linux with BPF support; suitable for CI on a capable kernel. |
| **Smoke / E2E** | Full stack test against a real Proxmox host. Validates end-to-end: VM boots, queries IMDS, receives correct metadata. Run manually or in a dedicated environment. |

Packages are designed for testability: interfaces are defined for the identity resolver, XDP manager, and backend client so they can be replaced with fakes in unit tests.

## Security considerations

- The main daemon runs with `CAP_NET_ADMIN` and `CAP_BPF` (or root) for XDP attachment. It should drop unnecessary capabilities after startup.
- The metadata backend runs as an unprivileged user and is only reachable via Unix socket with appropriate ownership/permissions.
- VM identity headers (`X-PVE-*`) must not be forwarded back to the guest.
- The eBPF program only redirects the exact `169.254.169.254:80` target; all other traffic passes through unmodified.
