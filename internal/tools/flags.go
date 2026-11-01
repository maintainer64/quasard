// SPDX-License-Identifier: GPL-3.0-only
package tools

import (
	"flag"
	"strings"
)

// FlagSet itself stops at first positional; keep Python's positional-before-flags
// spelling while still using stdlib flag parsing and validation.
func parseFlags(f *flag.FlagSet, args []string) error {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positionals = append(positionals, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		def := f.Lookup(name)
		if def == nil {
			continue
		}
		b, ok := def.Value.(interface{ IsBoolFlag() bool })
		if ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	flags = append(flags, "--")
	return f.Parse(append(flags, positionals...))
}
