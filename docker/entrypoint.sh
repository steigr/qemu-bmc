#!/bin/bash
set -e

# Runtime directories
mkdir -p /var/run/qemu /var/log/qemu

# --- QEMU argument construction ---
QEMU_ARGS=""

# Machine type and acceleration
if [ "${ENABLE_KVM:-true}" = "true" ] && [ -e /dev/kvm ] && [ -r /dev/kvm ] && [ -w /dev/kvm ]; then
    QEMU_ARGS="$QEMU_ARGS -machine q35,accel=kvm -cpu host"
else
    QEMU_ARGS="$QEMU_ARGS -machine q35,accel=tcg -cpu qemu64"
    [ "${ENABLE_KVM:-true}" = "true" ] && echo "WARN: KVM not available, falling back to TCG" >&2
fi

# Resources
QEMU_ARGS="$QEMU_ARGS -m ${VM_MEMORY:-2048} -smp ${VM_CPUS:-2}"

# Disk
VM_DISK="${VM_DISK:-/vm/disk.qcow2}"
if [ -n "$VM_DISK" ] && [ -f "$VM_DISK" ]; then
    QEMU_ARGS="$QEMU_ARGS -drive if=none,id=disk0,file=$VM_DISK,format=qcow2"
    QEMU_ARGS="$QEMU_ARGS -device virtio-blk-pci,drive=disk0,id=vdisk0,bootindex=2"
fi

# CD-ROM
if [ -n "$VM_CDROM" ] && [ -f "$VM_CDROM" ]; then
    QEMU_ARGS="$QEMU_ARGS -drive if=none,id=ide0-cd0,media=cdrom,file=$VM_CDROM,readonly=on"
    QEMU_ARGS="$QEMU_ARGS -device ich9-ahci,id=ahci0"
    QEMU_ARGS="$QEMU_ARGS -device ide-cd,drive=ide0-cd0,bus=ahci0.0,bootindex=1"
fi

# Boot device
BOOT_PARAM="${VM_BOOT:-c}"
if [ "${VM_BOOT_MENU_TIMEOUT:-0}" -gt 0 ] 2>/dev/null; then
    BOOT_PARAM="$BOOT_PARAM,menu=on,splash-time=${VM_BOOT_MENU_TIMEOUT}"
fi

# Boot mode (BIOS/UEFI)
case "${VM_BOOT_MODE:-bios}" in
    uefi)
        # OVMF pflash for UEFI boot
        OVMF_CODE="/usr/share/OVMF/OVMF_CODE.fd"
        OVMF_VARS="/vm/OVMF_VARS.fd"
        if [ ! -f "$OVMF_VARS" ]; then
            cp /usr/share/OVMF/OVMF_VARS.fd "$OVMF_VARS"
        fi
        QEMU_ARGS="$QEMU_ARGS -drive if=pflash,format=raw,readonly=on,file=$OVMF_CODE"
        QEMU_ARGS="$QEMU_ARGS -drive if=pflash,format=raw,file=$OVMF_VARS"
        ;;
    bios)
        QEMU_ARGS="$QEMU_ARGS -boot $BOOT_PARAM"
        # SGA device for serial console in BIOS mode
        QEMU_ARGS="$QEMU_ARGS -device sga"
        ;;
    *)
        echo "WARN: Unknown VM_BOOT_MODE '${VM_BOOT_MODE}', defaulting to bios" >&2
        QEMU_ARGS="$QEMU_ARGS -boot $BOOT_PARAM"
        QEMU_ARGS="$QEMU_ARGS -device sga"
        ;;
esac

# VNC
VNC_DISPLAY=$(( ${VNC_PORT:-5900} - 5900 ))
QEMU_ARGS="$QEMU_ARGS -vnc :$VNC_DISPLAY"

# Network
source /scripts/setup-network.sh

# Wait for explicitly specified interfaces to appear (containerlab creates them after container start)
if [ -n "$VM_NETWORKS" ]; then
    for iface in $(echo "$VM_NETWORKS" | tr ',' ' '); do
        timeout=30
        for i in $(seq 1 $timeout); do
            [ -e "/sys/class/net/$iface" ] && break
            [ "$i" -eq "$timeout" ] && echo "WARN: Interface $iface not found after ${timeout}s, proceeding without it" >&2
            sleep 1
        done
    done
fi

NET_ARGS=$(build_network_args 2>/dev/null || true)
if [ -n "$NET_ARGS" ]; then
    QEMU_ARGS="$QEMU_ARGS $NET_ARGS"
else
    QEMU_ARGS="$QEMU_ARGS -nic none"
fi

# Extra QEMU arguments (advanced users)
if [ -n "$QEMU_EXTRA_ARGS" ]; then
    QEMU_ARGS="$QEMU_ARGS $QEMU_EXTRA_ARGS"
fi

# Debug output
if [ "${DEBUG:-false}" = "true" ]; then
    echo "=== qemu-bmc startup ===" >&2
    echo "QEMU_ARGS: $QEMU_ARGS" >&2
    echo "IPMI_USER: ${IPMI_USER:-admin}" >&2
    echo "REDFISH_PORT: ${REDFISH_PORT:-443}" >&2
    echo "IPMI_PORT: ${IPMI_PORT:-623}" >&2
    echo "VM_BOOT_MODE: ${VM_BOOT_MODE:-bios}" >&2
    echo "========================" >&2
fi

# Launch qemu-bmc in process management mode (PID 1 via exec)
# BMC configuration (IPMI_USER, IPMI_PASS, REDFISH_PORT, etc.) is read
# directly from environment variables by qemu-bmc's config package.
# shellcheck disable=SC2086
exec qemu-bmc -- $QEMU_ARGS
