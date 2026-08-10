package main

import (
	"flag"
	"io"
)

// Flags holds CLI flags relevant to server startup mode.
// Admin subcommand handling is elsewhere and does not use this struct.
type Flags struct {
	Embedded bool
	Addr     string
}

// parseFlags parses args (without the program name). Returns the flags,
// any remaining positional args, and a parsing error.
// It silences the default flag error output so tests don't spam stderr.
func parseFlags(args []string) (Flags, []string, error) {
	var f Flags
	fs := flag.NewFlagSet("niuniu", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&f.Embedded, "embedded", false,
		"run in embedded mode (host-managed: localhost-only, auth off, mDNS off, heartbeat-pipe)")
	fs.StringVar(&f.Addr, "addr", "",
		"override listen address, e.g. 127.0.0.1:0 for ephemeral port")
	if err := fs.Parse(args); err != nil {
		return Flags{}, nil, err
	}
	return f, fs.Args(), nil
}
