package store

import "errors"

// ErrNotWorker: the session opted out of claiming tasks (worker=false at
// registration). Such sessions also never count tasks as claimable.
var ErrNotWorker = errors.New("session is not a worker (AGENTMESH_WORKER=off)")
