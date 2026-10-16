package api

import "context"

// Principal is the authenticated caller. Exactly one of these holds:
//   - Admin: the admin token (everything).
//   - Host != "" && !Session: a host token (the daemon: lease and load for
//     that host, plus read-only task/host/report access).
//   - Host != "" && Session: a session token (interactive Claude sessions on
//     that host: sessions, messages, task add and the same reads; no lease
//     operations, no cancel/requeue).
type Principal struct {
	Admin   bool
	Host    string
	Session bool
}

// CreatedBy is the value recorded in tasks.created_by.
func (p Principal) CreatedBy() string {
	if p.Admin {
		return "admin"
	}
	return "host:" + p.Host
}

type principalKey struct{}

// WithPrincipal stores p in ctx (the hub does this for every authenticated
// request, so MCP tool handlers can read it without importing hub).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored by WithPrincipal.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
