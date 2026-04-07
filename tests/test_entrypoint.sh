#!/bin/bash
# test_entrypoint.sh - Environment variable / entrypoint tests

test_default_memory() {
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-m 2048" "Default memory should be 2048"
}

test_custom_memory() {
    stop_test_container
    start_test_container -e "VM_MEMORY=4096"
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-m 4096" "Custom memory should be 4096"
}

test_default_cpus() {
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-smp 2" "Default CPUs should be 2"
}

test_custom_cpus() {
    stop_test_container
    start_test_container -e "VM_CPUS=4"
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-smp 4" "Custom CPUs should be 4"
}

test_kvm_enabled() {
    # Only run if KVM is available on host
    if [ ! -e /dev/kvm ]; then
        echo "SKIP: /dev/kvm not available"
        return 0
    fi
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "accel=kvm" "KVM should be enabled when /dev/kvm is available"
}

test_kvm_fallback_tcg() {
    stop_test_container
    # Start without /dev/kvm device
    docker rm -f "$TEST_CONTAINER" 2>/dev/null || true
    docker run -d \
        --name "$TEST_CONTAINER" \
        --device /dev/net/tun:/dev/net/tun \
        --cap-add NET_ADMIN \
        --cap-add SYS_ADMIN \
        --security-opt apparmor=unconfined \
        -p "${IPMI_PORT}:623/udp" \
        -p "${REDFISH_PORT}:443" \
        -e "IPMI_USER=${IPMI_USER}" \
        -e "IPMI_PASS=${IPMI_PASS}" \
        -e "ENABLE_KVM=true" \
        "$TEST_IMAGE"
    wait_for_container_running 30
    wait_for_qemu_running 60
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "accel=tcg" "Should fallback to TCG without /dev/kvm"
}

test_custom_vnc_port() {
    stop_test_container
    start_test_container -e "VNC_PORT=5901"
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-vnc :1" "VNC_PORT=5901 should produce -vnc :1"
}

test_disk_attached() {
    # Create a dummy disk in a temp directory
    local tmpdir
    tmpdir=$(mktemp -d)
    docker run --rm -v "$tmpdir:/mnt" "$TEST_IMAGE" qemu-img create -f qcow2 /mnt/test.qcow2 1G 2>/dev/null || \
    qemu-img create -f qcow2 "$tmpdir/test.qcow2" 1G 2>/dev/null || \
    truncate -s 1G "$tmpdir/test.qcow2"

    stop_test_container
    docker rm -f "$TEST_CONTAINER" 2>/dev/null || true
    docker run -d \
        --name "$TEST_CONTAINER" \
        --device /dev/kvm:/dev/kvm \
        --cap-add SYS_ADMIN \
        --security-opt apparmor=unconfined \
        -v "$tmpdir:/vm" \
        -p "${IPMI_PORT}:623/udp" \
        -p "${REDFISH_PORT}:443" \
        -e "IPMI_USER=${IPMI_USER}" \
        -e "IPMI_PASS=${IPMI_PASS}" \
        -e "VM_DISK=/vm/test.qcow2" \
        "$TEST_IMAGE"
    wait_for_container_running 30
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-drive" "Disk should be attached with -drive"
    rm -rf "$tmpdir"
}

test_disk_missing_no_error() {
    stop_test_container
    start_test_container -e "VM_DISK=/vm/nonexistent.qcow2"
    wait_for_container_running 30
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_not_contains "$cmdline" "-drive" "Missing disk should not add -drive"
}

test_cdrom_attached() {
    local tmpdir
    tmpdir=$(mktemp -d)
    truncate -s 1M "$tmpdir/test.iso"

    stop_test_container
    docker rm -f "$TEST_CONTAINER" 2>/dev/null || true
    docker run -d \
        --name "$TEST_CONTAINER" \
        --device /dev/kvm:/dev/kvm \
        --cap-add SYS_ADMIN \
        --security-opt apparmor=unconfined \
        -v "$tmpdir:/iso" \
        -p "${IPMI_PORT}:623/udp" \
        -p "${REDFISH_PORT}:443" \
        -e "IPMI_USER=${IPMI_USER}" \
        -e "IPMI_PASS=${IPMI_PASS}" \
        -e "VM_CDROM=/iso/test.iso" \
        "$TEST_IMAGE"
    wait_for_container_running 30
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "ide-cd" "CD-ROM should be attached as an explicit device"
    assert_contains "$cmdline" "bootindex=1" "CD-ROM device should be first in boot priority"
    rm -rf "$tmpdir"
}

test_boot_device_default() {
    stop_test_container
    start_test_container
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "-boot" "Default boot device should be set"
}

test_custom_ipmi_credentials() {
    stop_test_container
    docker rm -f "$TEST_CONTAINER" 2>/dev/null || true
    docker run -d \
        --name "$TEST_CONTAINER" \
        --device /dev/kvm:/dev/kvm \
        --cap-add SYS_ADMIN \
        --security-opt apparmor=unconfined \
        -p "${IPMI_PORT}:623/udp" \
        -p "${REDFISH_PORT}:443" \
        -e "IPMI_USER=testuser" \
        -e "IPMI_PASS=testpass123" \
        "$TEST_IMAGE"
    wait_for_container_running 30
    wait_for_qemu_running 30
    sleep 3
    local result
    result=$(ipmitool -I lanplus -H "$IPMI_HOST" -p "$IPMI_PORT" \
        -U testuser -P testpass123 mc info 2>&1)
    assert_contains "$result" "Device ID" "Custom IPMI credentials should work"
}

test_qemu_extra_args() {
    stop_test_container
    start_test_container -e "QEMU_EXTRA_ARGS=-device virtio-rng-pci"
    wait_for_qemu_running 30
    local cmdline
    cmdline=$(container_exec cat /proc/$(get_qemu_pid)/cmdline 2>/dev/null | tr '\0' ' ')
    assert_contains "$cmdline" "virtio-rng-pci" "Extra QEMU args should be passed"
}

test_debug_output() {
    stop_test_container
    start_test_container -e "DEBUG=true"
    wait_for_container_running 30
    sleep 3
    local logs
    logs=$(docker logs "$TEST_CONTAINER" 2>&1)
    assert_contains "$logs" "qemu-bmc startup" "Debug output should show startup info"
}

# --- Run tests ---
start_test_container
wait_for_ipmi_ready 60

run_test test_default_memory
run_test test_custom_memory
run_test test_default_cpus
run_test test_custom_cpus
run_test test_kvm_enabled
run_test test_kvm_fallback_tcg
run_test test_custom_vnc_port
run_test test_disk_attached
run_test test_disk_missing_no_error
run_test test_cdrom_attached
run_test test_boot_device_default
run_test test_custom_ipmi_credentials
run_test test_qemu_extra_args
run_test test_debug_output
