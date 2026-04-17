# NiceBlocks

A modern, Go-based alternative to `badblocks` with a beautiful web interface.

## Features

- **Fast & Modern:** Tailored for modern SSDs and HDDs.
- **Random Pattern Test:** Performs thorough read-write verification using random data patterns.
- **Web UI:** Provides a responsive dashboard using HTMX and Tailwind CSS.
- **Single Binary:** Compiles into a single static binary for easy deployment.
- **Service Ready:** Designed to run as a system service.

## Getting Started

### Prerequisites

- Go 1.22+

### Installation

```bash
go build -o niceblocks ./cmd/niceblocks
```

### Usage

Run the server:
```bash
sudo ./niceblocks
```
Access the UI at `http://localhost:8080`.

**Note:** Root privileges are required to access block devices directly.

## Development

- `make build`: Build the binary.
- `make run`: Run the server.
- `make test`: Run tests.
