# CLAUDE.md - qemu-bmc

## Project Overview

Go single binary that controls QEMU VMs via Redfish API (HTTPS) and IPMI over LAN (UDP:623).
Replaces [docker-qemu-bmc](https://github.com/tjst-t/docker-qemu-bmc) (shell scripts + ipmi_sim + supervisord).

Compatible with: MAAS, Tinkerbell/Rufio, Cybozu placemat

## Architecture

```
cmd/qemu-bmc/main.go          # Entrypoint, goroutines for Redfish + IPMI
internal/
  qmp/                         # QMP socket client (interface + implementation)
  qemu/                        # QEMU process management (args, process lifecycle)
  machine/                     # VM state management (wraps QMP + ProcessManager)
  redfish/                     # HTTP server with gorilla/mux, 15+ endpoints
  ipmi/                        # UDP server + VM chardev server, RMCP/RMCP+, RAKP auth, chassis commands
  bmc/                         # BMC configuration state (users, LAN, channels)
  config/                      # Environment variable config
docker/
  Dockerfile                   # Multi-stage build (Go builder + Debian runtime)
  entrypoint.sh                # Environment variables → QEMU args conversion
  setup-network.sh             # TAP/bridge network setup for VM passthrough
tests/                         # Bash-based container integration tests (82 tests)
  run_tests.sh                 # Test runner (category-based)
  test_helper.sh               # Assertions, container mgmt, IPMI/Redfish helpers
  test_*.sh                    # Test categories (container, ipmi, redfish, power, boot, etc.)
containerlab/
  example.yml                  # 2-node containerlab topology example
integration/                   # Go-based integration tests (legacy mode)
```

### Key Design Patterns

- `MachineInterface` - shared interface used by both Redfish and IPMI servers for testability
- `qmp.Client` - interface for QMP communication, mock implementation in `testutil_test.go`
- All tests use `testify/assert` and `testify/require`
- Mock QMP server (`qmp/testutil_test.go`) simulates full QMP protocol over UNIX socket
- Each Redfish endpoint handler is in a separate file (`handler_*.go`)

## Build & Test

```bash
# Go is not in default PATH in this environment
export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin

# Build
go build ./cmd/qemu-bmc

# Test (all packages)
go test ./... -count=1

# Test with race detector
go test ./... -race -count=1

# Test specific package
go test ./internal/redfish/... -v
go test ./internal/ipmi/... -v
go test ./internal/qmp/... -v

# Static analysis
go vet ./...

# Coverage
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out

# Docker build
make docker-build

# Container integration tests (quick smoke tests)
make container-test

# Container integration tests (all 82 tests across 9 categories)
make container-test-all

# Run specific test categories
./tests/run_tests.sh --build container ipmi redfish power boot
```

## Environment Variables

### BMC Configuration (Go binary)

| Variable | Default | Description |
|----------|---------|-------------|
| `QMP_SOCK` | `/var/run/qemu/qmp.sock` | QMP socket path |
| `IPMI_USER` | `admin` | IPMI/Redfish auth username |
| `IPMI_PASS` | `password` | IPMI/Redfish auth password |
| `REDFISH_ADDR` | `127.0.0.1` | Redfish bind address |
| `REDFISH_PORT` | `8080` | Redfish HTTP port |
| `IPMI_PORT` | `6623` | IPMI UDP port |
| `SERIAL_ADDR` | `localhost:9002` | SOL bridge target |
| `TLS_CERT` | (empty, optional) | TLS certificate path (TLS is enabled only when both `TLS_CERT` and `TLS_KEY` are set) |
| `TLS_KEY` | (empty, optional) | TLS key path |
| `VM_BOOT_MODE` | `bios` | Default boot mode |
| `VM_IPMI_ADDR` | (empty, disabled) | VM IPMI chardev listen address (e.g., `:9002`) |
| `POWER_ON_AT_START` | `false` | Power on VM automatically at container start |

### Container Configuration (entrypoint.sh)

| Variable | Default | Description |
|----------|---------|-------------|
| `VM_MEMORY` | `2048` | VM memory in MB |
| `VM_CPUS` | `2` | Number of VM CPUs |
| `ENABLE_KVM` | `true` | Use KVM acceleration (falls back to TCG) |
| `VNC_PORT` | `5900` | VNC display port |
| `VM_DISK` | `/vm/disk.qcow2` | VM disk image path |
| `VM_CDROM` | (empty) | CD-ROM ISO path |
| `VM_BOOT` | `c` | Boot device (c=disk, d=cdrom, n=network) |
| `VM_BOOT_MENU_TIMEOUT` | `0` | Boot menu timeout in ms (0=disabled) |
| `VM_NETWORKS` | (empty) | Comma-separated host interfaces for TAP passthrough |
| `QEMU_EXTRA_ARGS` | (empty) | Additional QEMU arguments |
| `DEBUG` | `false` | Enable debug output |

## Redfish Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/redfish/v1` | ServiceRoot |
| GET | `/redfish/v1/Systems` | System collection |
| GET | `/redfish/v1/Systems/1` | Computer system (PowerState, Boot, Actions) |
| PATCH | `/redfish/v1/Systems/1` | Boot device override (with ETag support) |
| POST | `/redfish/v1/Systems/1/Actions/ComputerSystem.Reset` | Power control |
| GET | `/redfish/v1/Managers` | Manager collection |
| GET | `/redfish/v1/Managers/1` | BMC manager |
| GET | `/redfish/v1/Managers/1/VirtualMedia` | VirtualMedia collection |
| GET | `/redfish/v1/Managers/1/VirtualMedia/CD1` | VirtualMedia resource |
| POST | `.../VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia` | Insert ISO |
| POST | `.../VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia` | Eject ISO |
| GET | `/redfish/v1/Chassis` | Chassis collection |
| GET | `/redfish/v1/Chassis/1` | Chassis resource |

Middleware: Basic Auth, ETag (If-Match), trailing slash normalization

## IPMI Commands

| Command | NetFn | Cmd | Description |
|---------|-------|-----|-------------|
| Get Device ID | App (0x06) | 0x01 | Static BMC identity |
| Get Channel Auth Capabilities | App | 0x38 | Auth type negotiation |
| Set Session Privilege | App | 0x3B | Privilege level |
| Close Session | App | 0x3C | Session teardown |
| Get Chassis Status | Chassis (0x00) | 0x01 | Power state query |
| Chassis Control | Chassis | 0x02 | Power on/off/cycle/reset |
| Chassis Identify | Chassis | 0x04 | Identify LED (log only) |
| Set Boot Options | Chassis | 0x08 | Boot device override |
| Get Boot Options | Chassis | 0x09 | Boot device query |

Supports: RMCP v1.0, IPMI 1.5, RMCP+ with RAKP HMAC-SHA1 auth, AES-CBC-128 encryption

## VM IPMI (In-Band)

Supports QEMU's `ipmi-bmc-extern` device for in-band IPMI communication from the guest OS via KCS interface. This enables MaaS commissioning scripts to configure BMC users, LAN settings, and channel access from within the VM.

### QEMU Configuration

```bash
qemu-system-x86_64 \
  -chardev socket,id=ipmi0,host=localhost,port=9002,reconnect=10 \
  -device ipmi-bmc-extern,id=bmc0,chardev=ipmi0 \
  -device isa-ipmi-kcs,bmc=bmc0 \
  ...
```

### Supported In-Band Commands

| Command | NetFn | Cmd | Description |
|---------|-------|-----|-------------|
| Get User Access | App (0x06) | 0x44 | Read user privileges |
| Get User Name | App (0x06) | 0x46 | Read username |
| Set User Name | App (0x06) | 0x45 | Set username |
| Set User Password | App (0x06) | 0x47 | Set password |
| Set User Access | App (0x06) | 0x43 | Set user privileges |
| Get Channel Access | App (0x06) | 0x41 | Read channel settings |
| Set Channel Access | App (0x06) | 0x40 | Write channel settings |
| Get Channel Info | App (0x06) | 0x42 | Channel information |
| Get LAN Config | Transport (0x0C) | 0x02 | Read LAN parameters |
| Set LAN Config | Transport (0x0C) | 0x01 | Write LAN parameters |

Plus all existing chassis and app commands (Get Device ID, Chassis Status, Boot Options, etc.)

Uses the OpenIPMI VM wire protocol over TCP with framing (0xA0/0xA1), byte escaping (0xAA), and two's complement checksums.

## GitHub Pages

- Site URL: https://tjst-t.github.io/qemu-bmc/
- `gh-pages` branch (orphan) に `index.html` を配置
- main ブランチから `docs/gh-pages/` に git submodule として参照可能
- サイト更新手順:
  1. `docs/gh-pages/index.html` を編集
  2. submodule 内でコミット&プッシュ:
     ```bash
     cd docs/gh-pages
     git add -A && git commit -m "Update site" && git push origin gh-pages
     cd ../..
     ```
  3. main 側で submodule 参照を更新:
     ```bash
     git add docs/gh-pages && git commit -m "chore: update gh-pages submodule ref"
     ```

## Notes for Claude Code

- `grep`, `tail`, `head` are not available as bash commands in the Claude Code sandbox — use the dedicated Read/Grep tools instead

## Development Guidelines

- TDD: Write tests first, verify RED, implement, verify GREEN, refactor
- All Redfish JSON must include `@odata.type`, `@odata.id`, `@odata.context` for gofish compatibility
- ETag: Return in both header and `@odata.etag` field; accept PATCH without If-Match (MAAS compat)
- Trailing slashes: Both `/path` and `/path/` must work identically
- IPMI checksums use two's complement: `0x100 - (sum & 0xFF)`
- All multi-byte IPMI fields are little-endian
