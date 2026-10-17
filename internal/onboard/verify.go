package onboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/felixn678/agent-mesh/internal/client"
)

// verifyToken makes one authenticated read (GET /api/v1/hosts, open to every
// token kind) and turns failures into actionable messages.
func verifyToken(ctx context.Context, hubURL, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := client.New(hubURL, token).Hosts(ctx)
	return explainHubErr(hubURL, err)
}

// verifyKind checks that token is accepted and is of the given kind
// ("admin", "host" or "session"): a swapped token passes verifyToken but
// every call of the wrong role is refused with 403.
func verifyKind(ctx context.Context, hubURL, token, kind string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	who, err := client.New(hubURL, token).WhoAmI(ctx)
	if err := explainHubErr(hubURL, err); err != nil {
		return err
	}
	if who.Kind != kind {
		return fmt.Errorf("token is a %s token but a %s token is required (swapped files? re-run agent-mesh join)", who.Kind, kind)
	}
	return nil
}

func explainHubErr(hubURL string, err error) error {
	var he *client.HTTPError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &he) && he.Status == http.StatusUnauthorized:
		return fmt.Errorf("hub at %s rejected the token (401): the hub does not know it yet, or it was revoked", hubURL)
	case errors.As(err, &he):
		return fmt.Errorf("hub at %s answered: %w", hubURL, err)
	}
	return fmt.Errorf("cannot reach hub at %s: %w\n%s", hubURL, err, unreachableHint(hubURL))
}

// tailnetRange is Tailscale's address block (100.64.0.0/10).
var tailnetRange = netip.MustParsePrefix("100.64.0.0/10")

// unreachableHint says what to check when the hub does not answer; for a
// Tailscale address the usual cause is this machine not being on the tailnet.
func unreachableHint(hubURL string) string {
	host := hubURL
	if u, err := url.Parse(hubURL); err == nil {
		host = u.Hostname()
	}
	if ip, err := netip.ParseAddr(host); err == nil && tailnetRange.Contains(ip) {
		return "hint: " + host + " is a Tailscale address. Make sure Tailscale is installed and Connected on this machine, " +
			"logged in with the same account as the hub machine (`tailscale status` must list the hub)."
	}
	return "hint: check that the hub is running (`agent-mesh doctor` on the hub machine) and that this machine can reach that address."
}

// verifyEventually retries verifyToken for up to timeout; used after a SIGHUP
// reload, which applies asynchronously.
func verifyEventually(ctx context.Context, hubURL, token string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := verifyToken(ctx, hubURL, token)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
