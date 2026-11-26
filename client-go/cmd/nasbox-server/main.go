// Command nasbox-server is the in-house replacement for the bash server
// script. Milestone 0 implements read-only verbs with byte-for-byte
// compatibility so it can run side by side with the existing daemon.
package main

import (
	"fmt"
	"os"

	"nasbox-go/internal/serverd"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opts, rest, err := serverd.ParseOptions(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return serverd.Usage()
	}
	ctx, err := serverd.Open(opts)
	if err != nil {
		return err
	}
	defer ctx.Close()
	return serverd.Dispatch(ctx, rest[0], rest[1:])
}
