package qemu

import (
	"fmt"
	"strings"
)

// forbiddenArgs are QEMU arguments that qemu-bmc manages itself.
var forbiddenArgs = []string{"-qmp", "-daemonize"}

// forbiddenArgValues maps arguments to forbidden value prefixes.
var forbiddenArgValues = map[string]func(string) bool{
	"-serial": func(_ string) bool { return true },
	"-chardev": func(val string) bool {
		return strings.Contains(val, "id=serial0")
	},
	"-monitor": func(val string) bool {
		return val == "stdio"
	},
}

// ValidateArgs checks that user-provided QEMU arguments don't conflict
// with arguments that qemu-bmc manages.
func ValidateArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]

		for _, forbidden := range forbiddenArgs {
			if arg == forbidden {
				return fmt.Errorf("argument %q is managed by qemu-bmc and must not be specified", arg)
			}
		}

		if checker, ok := forbiddenArgValues[arg]; ok {
			if i+1 < len(args) {
				val := args[i+1]
				if checker(val) {
					return fmt.Errorf("argument %q with value %q is managed by qemu-bmc and must not be specified", arg, val)
				}
			}
		}
	}
	return nil
}

// defaultArgs are added when not already present in user args.
var defaultArgs = []struct {
	flag     string
	defaults []string
}{
	{"-machine", []string{"-machine", "q35"}},
	{"-m", []string{"-m", "2048"}},
	{"-smp", []string{"-smp", "2"}},
	{"-vga", []string{"-vga", "std"}},
}

// ApplyDefaults adds default QEMU arguments for flags not already present.
func ApplyDefaults(args []string) []string {
	present := make(map[string]bool)
	for _, arg := range args {
		present[arg] = true
	}

	result := make([]string, len(args))
	copy(result, args)

	for _, d := range defaultArgs {
		if !present[d.flag] {
			// Skip -vga when -nographic is used
			if d.flag == "-vga" && present["-nographic"] {
				continue
			}
			result = append(result, d.defaults...)
		}
	}
	return result
}

// BuildOptions configures auto-injected QEMU arguments.
type BuildOptions struct {
	QMPSocketPath string
	SerialAddr    string
}

// BuildCommandLine validates user args, applies defaults, and injects
// qemu-bmc-managed arguments (QMP, serial, display).
func BuildCommandLine(userArgs []string, opts BuildOptions) ([]string, error) {
	if err := ValidateArgs(userArgs); err != nil {
		return nil, err
	}

	args := ApplyDefaults(userArgs)

	// Inject QMP socket
	args = append(args,
		"-qmp", fmt.Sprintf("unix:%s,server,nowait", opts.QMPSocketPath),
	)

	// Inject display none (unless -nographic already set)
	hasNographic := false
	for _, a := range args {
		if a == "-nographic" {
			hasNographic = true
			break
		}
	}
	if !hasNographic {
		args = append(args, "-display", "none")
	}

	// Inject serial console
	if opts.SerialAddr != "" {
		host, port, found := strings.Cut(opts.SerialAddr, ":")
		if !found {
			return nil, fmt.Errorf("invalid serial address %q: expected host:port", opts.SerialAddr)
		}
		args = append(args,
			"-chardev", fmt.Sprintf("socket,id=serial0,host=%s,port=%s,server=on,wait=off", host, port),
			"-serial", "chardev:serial0",
		)
	}

	return args, nil
}

// bootTargetToQEMU maps Redfish boot targets to QEMU -boot arguments.
var bootTargetToQEMU = map[string]string{
	"Pxe":       "n",
	"Hdd":       "c",
	"Cd":        "d",
	"BiosSetup": "menu=on",
}

// ApplyBootOverride modifies QEMU args to apply a boot target override.
// If target is "None" or empty, args are returned unchanged.
func ApplyBootOverride(args []string, target string) []string {
	bootVal, ok := bootTargetToQEMU[target]
	if !ok {
		return args
	}

	result := make([]string, 0, len(args)+2)
	replaced := false
	for i := 0; i < len(args); i++ {
		if args[i] == "-boot" && i+1 < len(args) {
			result = append(result, "-boot", bootVal)
			i++ // skip old value
			replaced = true
		} else {
			result = append(result, args[i])
		}
	}

	if !replaced {
		result = append(result, "-boot", bootVal)
	}
	return result
}
