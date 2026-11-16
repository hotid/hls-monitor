// Package envflag declares command-line flags whose defaults come from
// environment variables, so that a program is configured the same way on
// the command line and in a container.
package envflag

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

// String declares a string flag; env, when set, overrides def.
func String(fs *flag.FlagSet, p *string, name, env, def, usage string) {
	if v, ok := os.LookupEnv(env); ok {
		def = v
	}
	fs.StringVar(p, name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

// Duration declares a duration flag; env, when set to a valid duration,
// overrides def. An invalid value stops the program: silently falling back
// to the default would hide a configuration mistake.
func Duration(fs *flag.FlagSet, p *time.Duration, name, env string, def time.Duration, usage string) {
	if v, ok := os.LookupEnv(env); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid duration in %s: %q\n", env, v)
			os.Exit(2)
		}
		def = d
	}
	fs.DurationVar(p, name, def, fmt.Sprintf("%s (env %s)", usage, env))
}

// Int declares an integer flag; env, when set to a valid number, overrides
// def. An invalid value stops the program, as with Duration.
func Int(fs *flag.FlagSet, p *int, name, env string, def int, usage string) {
	if v, ok := os.LookupEnv(env); ok && v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid number in %s: %q\n", env, v)
			os.Exit(2)
		}
		def = n
	}
	fs.IntVar(p, name, def, fmt.Sprintf("%s (env %s)", usage, env))
}
