package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSignalName(t *testing.T) {
	sig, err := parseSignalName("USR2")
	require.NoError(t, err)
	assert.Equal(t, syscall.SIGUSR2, sig)

	sig, err = parseSignalName("sigusr1")
	require.NoError(t, err)
	assert.Equal(t, syscall.SIGUSR1, sig)

	_, err = parseSignalName("TERM")
	require.Error(t, err)
}

func TestParseGovernanceOptions(t *testing.T) {
	opts, err := parseGovernanceOptions([]string{"--reset-signal", "USR2", "--", "-machine", "q35"})
	require.NoError(t, err)
	assert.Equal(t, syscall.SIGUSR2, opts.resetSignal)
	assert.Equal(t, []string{"-machine", "q35"}, opts.childArgs)
}

func TestResetVarsFile(t *testing.T) {
	dir := t.TempDir()
	tpl := filepath.Join(dir, "template.fd")
	vars := filepath.Join(dir, "vars.fd")

	require.NoError(t, os.WriteFile(tpl, []byte("template-content"), 0o644))
	require.NoError(t, os.WriteFile(vars, []byte("old-content"), 0o644))

	require.NoError(t, resetVarsFile(vars, tpl))

	data, err := os.ReadFile(vars)
	require.NoError(t, err)
	assert.Equal(t, "template-content", string(data))
}

