package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/felixn678/agent-mesh/internal/client"
	"github.com/felixn678/agent-mesh/internal/mcptools/format"
)

const hostNoteUsage = `usage: agent-mesh host-note [--host <machine>] <text>    set what a machine is for
       agent-mesh host-note [--host <machine>] --clear      remove the note
       agent-mesh host-note                                 show every machine's note and tools`

// runHostNote sets the role note sessions read when choosing target_host.
// Without --host it describes this machine (from its session token); another
// machine needs the admin token, which exists only on the hub machine.
func runHostNote(ctx context.Context, std cmdIO, args []string) error {
	fs := newFlags("host-note", std)
	host := fs.String("host", "", "machine to describe (default: this machine; another machine needs the hub's admin token)")
	clear := fs.Bool("clear", false, "remove the note")
	if err := fs.Parse(args); err != nil {
		return err
	}
	note := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if note == "" && !*clear {
		if *host != "" {
			return errors.New(hostNoteUsage)
		}
		return printHostProfiles(ctx, std)
	}
	if note != "" && *clear {
		return errors.New("pass either a note or --clear\n" + hostNoteUsage)
	}
	c, err := cliClient()
	if *host != "" {
		c, err = adminClient()
	}
	if err != nil {
		return err
	}
	name := *host
	if name == "" {
		who, err := c.WhoAmI(ctx)
		if err != nil {
			return err
		}
		if who.Host == "" {
			return errors.New("this machine has no session token, so its machine name is unknown: pass --host <machine>")
		}
		name = who.Host
	}
	if err := c.SetHostNote(ctx, name, note); err != nil {
		return adminHint("host-note --host", err)
	}
	if note == "" {
		fmt.Fprintf(std.out, "cleared the note of %s\n", name)
	} else {
		fmt.Fprintf(std.out, "%s: %q\nnew Claude sessions on every machine will see it\n", name, note)
	}
	return nil
}

func printHostProfiles(ctx context.Context, std cmdIO) error {
	c, err := cliClient()
	if err != nil {
		return err
	}
	return writeHostProfiles(ctx, std, c)
}

func writeHostProfiles(ctx context.Context, std cmdIO, c *client.Client) error {
	list, err := c.HostProfiles(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(std.out, "no machines yet (a machine appears once a Claude session or daemon runs on it)")
		return nil
	}
	fmt.Fprintln(std.out, "machines:")
	for _, h := range list {
		fmt.Fprintln(std.out, "  "+format.HostProfileLine(h))
	}
	return nil
}
