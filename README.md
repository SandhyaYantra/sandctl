# sandctl

A small terminal control center for Linux, written in Go. `sandctl` brings a few everyday system controls and live machine stats into one fullscreen TUI, with a layout inspired by tools like `btop`.

## Features

- Fullscreen, two-panel interface with keyboard navigation.
- Audio volume control through `wpctl`, with a live percentage indicator.
- Screen brightness control through `brightnessctl`.
- Wi-Fi network scanning with signal strength and connection status. Selecting a network opens actions to connect, disconnect, or show a saved password through NetworkManager.
- Paired Bluetooth device list and connection through `bluetoothctl`.
- Live system dashboard, refreshed every second:
  - Per-core CPU load and load averages.
  - RAM usage details and a short memory history graph.
  - Root filesystem usage.
  - Network interface, local IP, traffic rates, totals, and traffic graphs.
  - Battery, temperature, and Intel GPU readings when the system exposes them.

Some hardware metrics depend on available kernel interfaces and permissions. Missing readings are shown as unavailable; Intel GPU utilization may be estimated from clock frequency when direct telemetry cannot be read.

## Requirements

- Go 1.24 or newer.
- Linux with a terminal that supports alternate screen and ANSI colors.
- NetworkManager tools for Wi-Fi: `nmcli`.
- Bluetooth control utility: `bluetoothctl`.
- PipeWire control utility for audio: `wpctl`.
- `brightnessctl` for backlight control.

The system dashboard uses [`gopsutil`](https://github.com/shirou/gopsutil) for CPU, memory, disk, load average, and network counters. Battery, temperature, and Intel GPU details are read from sysfs when available.

## Build and Run

```sh
go build -o sandctl .
./sandctl
```

Or run it directly from the project directory:

```sh
go run .
```

## Controls

| Key | Action |
| --- | --- |
| `Up` / `Down`, `j` / `k` | Move through the main menu or a device list |
| `Left` / `Right`, `-` / `+` | Lower or raise audio volume or brightness |
| `Enter` | Open Wi-Fi or Bluetooth, or choose an action in the Wi-Fi dialog |
| `Esc` | Return from a submenu or close the Wi-Fi dialog |
| `q` / `Ctrl+C` | Quit |

## Development Status

This is an early-stage project and is still being developed. The core screens and controls are in place, but behavior has not been polished across different Linux distributions, hardware, or desktop setups. Some controls require the relevant system service, command-line tool, or device permissions to be configured. Expect rough edges and occasional missing metrics.

## Author

**Sandhyayantra**

- Instagram: [@sandhyayantra](https://www.instagram.com/sandhyayantra/)
- Facebook: [sandhyayantra](https://www.facebook.com/sandhyayantra)
- TikTok: [@sandhyayantra](https://www.tiktok.com/@sandhyayantra)
- GitHub: [@sandhyayantra](https://github.com/sandhyayantra)
- LinkedIn: Dian Septio
