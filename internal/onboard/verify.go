package onboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/felixn678/agent-mesh/internal/client"
)

// verifyToken makes one authenticated read (GET /api/v1/hosts, open to every
// token kind) and turns failures into actionable messages.
func verifyToken(ctx context.Context, hubURL, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := client.New(hubURL, token).Hosts(ctx)
	var he *client.HTTPError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &he) && he.Status == http.StatusUnauthorized:
		return fmt.Errorf("hub at %s rejected the token (401): the hub does not know it yet, or it was revoked", hubURL)
	case errors.As(err, &he):
		return fmt.Errorf("hub at %s answered: %w", hubURL, err)
	}
	return fmt.Errorf("cannot reach hub at %s: %w", hubURL, err)
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
