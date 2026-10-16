package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/felixn678/agent-mesh/internal/api"
	"github.com/felixn678/agent-mesh/internal/config"
)

// runToken implements `token new --admin | --host <name> | --session <name> --out <file>`: it
// writes a fresh token (0600, never overwriting) and prints the hub.toml line
// holding its hash. The token itself is never printed.
func runToken(stdout io.Writer, args []string) error {
	if len(args) == 0 || args[0] != "new" {
		return errors.New("usage: token new (--admin | --host <name> | --session <name>) --out <file>")
	}
	fs := flag.NewFlagSet("token new", flag.ContinueOnError)
	admin := fs.Bool("admin", false, "create the admin token")
	host := fs.String("host", "", "create a daemon token for this host name")
	session := fs.String("session", "", "create a session token (interactive Claude sessions) for this host name")
	out := fs.String("out", "", "file to write the token to (mode 0600)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	kinds := 0
	for _, set := range []bool{*admin, *host != "", *session != ""} {
		if set {
			kinds++
		}
	}
	if kinds != 1 {
		return errors.New("specify exactly one of --admin, --host <name> or --session <name>")
	}
	name := *host + *session
	if name != "" && (!api.ValidHostName(name) || name == "admin") {
		return fmt.Errorf("invalid host name %q", name)
	}
	if *out == "" {
		return errors.New("--out <file> is required")
	}
	tok, err := config.NewToken()
	if err != nil {
		return err
	}
	if err := config.WriteToken(*out, tok); err != nil {
		return err
	}
	if *admin {
		fmt.Fprintf(stdout, "admin_token_hash = %q\n", config.HashToken(tok))
	} else if *host != "" {
		fmt.Fprintf(stdout, "%s = %q   # under [host_tokens]\n", name, config.HashToken(tok))
	} else {
		fmt.Fprintf(stdout, "%s = %q   # under [session_tokens]\n", name, config.HashToken(tok))
	}
	return nil
}
