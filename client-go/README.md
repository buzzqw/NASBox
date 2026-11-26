# NASBox Go — source

This folder holds the source of the desktop client (`cmd/nasbox`) and of the NAS
daemon (`cmd/nasbox-server`), written in Go.

- **To use NASBox**: read the [user manual](../MANUAL.md) and download the programs
  from the [latest release](../../../releases/latest).
- **To build it and contribute**: see the [main README](../README.md) and
  [CONTRIBUTING.md](../CONTRIBUTING.md).

```bash
go vet ./... && go test ./...
go build -trimpath -ldflags='-s -w' -o nasbox-go ./cmd/nasbox            # needs CGO
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o nasbox-server ./cmd/nasbox-server
```
