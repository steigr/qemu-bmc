# hack launcher

`hack/qemu_bmc.go` is the self-contained Go replacement for `hack/qemu-bmc.sh`.

It starts a QEMU VM, waits for QMP readiness, then runs `qemu-bmc` in legacy mode with the right environment.

## Quick try

```bash
cd /Users/steigr/Developer/github.com/steigr/qemu-bmc

go run ./hack/qemu_bmc.go --uefi
```

You can still run the shell entrypoint; it now delegates to the Go tool:

```bash
bash ./hack/qemu-bmc.sh --uefi
```

## Flags

- `--arch <arch>`: `x86_64`, `aarch64`, `arm`, `riscv64`, `ppc64`, `s390x`
- `--uefi`: force UEFI mode
- `--iso <path>`: attach ISO as CD-ROM
- `--disk <path>`: disk image path
- `--port <port>`: Redfish port

## Environment variables

- `VM_ARCH`
- `VM_BOOT_MODE` (`bios` or `uefi`)
- `OVMF_CODE`, `OVMF_VARS`
- `QEMU_DISK`, `QEMU_DISK_SIZE`
- `QEMU_MEMORY`, `QEMU_CPUS`
- `QEMU_ISO`
- `REDFISH_ADDR` (default `127.0.0.1`)
- `REDFISH_PORT` (default `8080`)
- `IPMI_PORT` (default `6623`)
- `IPMI_USER`, `IPMI_PASS`
- `VNC_PORT`, `SSH_PORT`
- `QEMU_BMC_BIN` (optional absolute/relative path to the `qemu-bmc` executable)

## Notes

- The tool probes QEMU device support for in-band IPMI (`ipmi-bmc-extern`) and enables it when available.
- For UEFI on `q35`, firmware pairs with combined size >= 8 MiB are skipped.
- The launcher prefers `./qemu-bmc` in the repo root, then falls back to `go run ./cmd/qemu-bmc` to avoid using a stale binary from `PATH`.

