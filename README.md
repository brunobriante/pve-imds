# PVE IMDS - TAP Device Watcher

A simple Go application that uses netlink to monitor TAP device creation and deletion events on a Linux hypervisor host. This is useful for tracking when virtual machines are created or deleted, as they typically create/destroy TAP devices for network connectivity.

## Features

- Real-time monitoring of TAP device creation and deletion
- Uses netlink sockets for efficient kernel event monitoring
- Graceful shutdown with signal handling
- Linux-specific implementation using the mdlayher/netlink package

## Requirements

- Linux operating system
- Go 1.24.5 or later
- Root privileges (for netlink socket access)

## Installation

1. Clone the repository:
```bash
git clone <repository-url>
cd pve-imds
```

2. Build the application:
```bash
go build -o pve-imds .
```

## Usage

Run the application with root privileges to access netlink sockets:

```bash
sudo ./pve-imds
```

The application will start monitoring for TAP device events and log when devices are created or deleted:

```
2024/01/XX XX:XX:XX TAP device watcher started. Press Ctrl+C to stop.
2024/01/XX XX:XX:XX Started watching for TAP device events...
2024/01/XX XX:XX:XX TAP device created: tap100i0
2024/01/XX XX:XX:XX TAP device deleted: tap100i0
```

Press `Ctrl+C` to gracefully shut down the application.

## How it Works

The application:

1. Opens a netlink socket connection to the kernel
2. Subscribes to `RTNLGRP_LINK` multicast group to receive network interface events
3. Parses incoming netlink messages to extract interface information
4. Filters for TAP devices (interfaces starting with "tap")
5. Logs creation (`RTM_NEWLINK`) and deletion (`RTM_DELLINK`) events

## Dependencies

- `github.com/mdlayher/netlink` - Netlink socket communication
- `golang.org/x/sys/unix` - Unix system calls and constants

## License

[Add your license information here] 