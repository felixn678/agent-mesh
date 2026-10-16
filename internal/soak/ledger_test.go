//go:build soak

package soak

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/felixn678/agent-mesh/internal/client"
)

// ledger is the client-side record of everything the actors observed. The
// invariants compare it with the hub database at the end.
type ledger struct {
	mu sync.Mutex

	added     map[int64]addRec    // acknowledged task_add
	claims    map[[2]int64]string // (task, fence) -> session holding it
	completes map[int64][]int64   // accepted (not replayed) completes: task -> fences
	sent      map[int64]string    // message id -> recipient session
	delivered map[int64]int       // message id -> times shown to its recipient
	acked     map[int64]bool      // message ids acked successfully
	late      map[string]int      // late-complete outcomes
	errs      map[string]int      // client errors by kind
	status500 int                 // responses with HTTP 500
	violation []string            // invariant breaks seen live

	claimLat   []time.Duration // claim request latency
	addToClaim []time.Duration // task_add -> first claim
	wakeLat    []time.Duration // task_add during a wait -> waiter returned claimable
}

type addRec struct {
	at      time.Time
	creator string
}

func newLedger() *ledger {
	return &ledger{added: map[int64]addRec{}, claims: map[[2]int64]string{}, completes: map[int64][]int64{},
		sent: map[int64]string{}, delivered: map[int64]int{}, acked: map[int64]bool{},
		late: map[string]int{}, errs: map[string]int{}}
}

func (l *ledger) violate(format string, a ...any) {
	l.mu.Lock()
	l.violation = append(l.violation, fmt.Sprintf(format, a...))
	l.mu.Unlock()
}

// noteErr classifies a client error; it returns true for errors worth a
// retry (network, 429, 5xx: the hub may be restarting).
func (l *ledger) noteErr(op string, err error) (retry bool) {
	if err == nil {
		return false
	}
	var he *client.HTTPError
	kind := "net"
	switch {
	case errors.Is(err, client.ErrNoTask):
		return false
	case client.IsLeaseLost(err):
		kind = "lease"
	case errors.As(err, &he):
		kind = fmt.Sprintf("http%d:%s", he.Status, he.Code)
	}
	l.mu.Lock()
	l.errs[op+":"+kind]++
	if he != nil && he.Status == 500 {
		l.status500++
	}
	l.mu.Unlock()
	return kind == "net" || (he != nil && (he.Status == 429 || he.Status >= 500))
}

func (l *ledger) recordClaim(task, fence int64, holder string, lat time.Duration, waitStart time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := [2]int64{task, fence}
	if prev, ok := l.claims[key]; ok && prev != holder {
		l.violation = append(l.violation, fmt.Sprintf("task %d fence %d claimed by %s and %s", task, fence, prev, holder))
	}
	l.claims[key] = holder
	l.claimLat = append(l.claimLat, lat)
	if a, ok := l.added[task]; ok && fence == 1 {
		l.addToClaim = append(l.addToClaim, time.Since(a.at))
		if !waitStart.IsZero() && a.at.After(waitStart) {
			l.wakeLat = append(l.wakeLat, time.Since(a.at)-lat)
		}
	}
}

func (l *ledger) recordDelivery(ids []int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, id := range ids {
		if l.acked[id] {
			l.violation = append(l.violation, fmt.Sprintf("message %d re-delivered after ack", id))
		}
		l.delivered[id]++
	}
}

func (l *ledger) recordAck(ids []int64) {
	l.mu.Lock()
	for _, id := range ids {
		l.acked[id] = true
	}
	l.mu.Unlock()
}

func (l *ledger) recordComplete(task, fence int64) {
	l.mu.Lock()
	l.completes[task] = append(l.completes[task], fence)
	l.mu.Unlock()
}

func pct(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[min(len(s)-1, int(float64(len(s))*p))]
}
