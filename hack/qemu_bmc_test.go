package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeArch(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "amd64", want: "x86_64"},
		{in: "x86_64", want: "x86_64"},
		{in: "arm64", want: "aarch64"},
		{in: "armv7l", want: "arm"},
		{in: "riscv64", want: "riscv64"},
		{in: "ppc64le", want: "ppc64"},
		{in: "s390x", want: "s390x"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := normalizeArch(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeArch_Invalid(t *testing.T) {
	_, err := normalizeArch("mips64")
	require.Error(t, err)
}

func TestProfileForArch(t *testing.T) {
	p, err := profileForArch("x86_64")
	require.NoError(t, err)
	assert.Equal(t, "qemu-system-x86_64", p.QEMUBin)
	assert.Equal(t, "q35", p.Machine)
	assert.Equal(t, "virtio-net-pci", p.NetDevice)
}

func TestBuildCDROMArgs(t *testing.T) {
	cdromArgs, bootArgs, err := buildCDROMArgs("q35", "/tmp/test.iso")
	require.NoError(t, err)
	assert.Contains(t, cdromArgs, "ich9-ahci,id=ahci0")
	assert.Contains(t, cdromArgs, "ide-cd,drive=ide0-cd0,bus=ahci0.0,bootindex=1")
	assert.Empty(t, bootArgs)
}
