# NiceBlocks

A modern, Go-based alternative to `badblocks` with a real-time web interface.

## Features

- **Random Pattern Test** — Read-write verification using AES-CTR keystream (hardware-accelerated via AES-NI)
- **Web Dashboard** — Responsive UI built with HTMX and Tailwind CSS, with live progress grid, throughput heatmap, and SMART details
- **SMART Monitoring** — Temperature-based thermal throttling with auto-pause/resume and critical abort
- **Protocol Support** — NVMe, SATA (including USB-to-SATA bridges), and SCSI drives
- **Single Binary** — Compiles into a zero-dependency static binary
- **CSRF Protection** — Double-submit cookie pattern on all state-changing requests
- **Optional Auth** — HTTP Basic Auth via `PASSWORD` env var or `-password` flag

## Installation

### From .deb package (Debian/Ubuntu)

Download the package for your architecture from the [latest release](https://github.com/robindubreuil/niceblocks/releases/latest), then:

```bash
sudo dpkg -i niceblocks_*.deb
```

### From source

Requires Go 1.26+:

```bash
git clone https://github.com/robindubreuil/niceblocks.git
cd niceblocks
go build -o niceblocks ./cmd/niceblocks
```

## Usage

```bash
sudo niceblocks
```

Access the dashboard at `http://localhost:8080`.

> Root privileges are required to access block devices directly.

### Options

| Flag       | Env var   | Default | Description                          |
|------------|-----------|---------|--------------------------------------|
| `-port`    | `PORT`    | `8080`  | Port to listen on                    |
| `-password`| `PASSWORD`| *(none)*| Optional UI password (HTTP Basic Auth)|

### Systemd

```bash
sudo systemctl enable --now niceblocks
```

Configure port and password via environment variables in the service override:

```bash
sudo systemctl edit niceblocks
```

```ini
[Service]
Environment=PORT=8080
Environment=PASSWORD=your-password
```

## Supported Architectures

Pre-built `.deb` packages are available for:

| Architecture | Debian name | Use case                     |
|--------------|-------------|------------------------------|
| x86-64       | `amd64`     | PCs, servers                 |
| ARM64        | `arm64`     | Raspberry Pi 4/5, ARM servers|
| ARM hard-float| `armhf`    | Older Raspberry Pi, ARM boards|
| RISC-V 64    | `riscv64`   | RISC-V boards and SBCs       |
| ARM soft-float| `armel`    | Old NAS, legacy ARM devices   |
| x86 (32-bit) | `i386`      | Legacy 32-bit systems        |

## Development

```bash
make build    # Build the binary
make run      # Build and run (requires sudo)
make test     # Run tests with race detector
make clean    # Remove build artifacts
```

## License

This project includes code from [smart.go](https://github.com/anatol/smart.go) (see `local/smart.go/LICENSE`).
