package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// errParentGone cancels a waiter whose Claude session is gone: it must exit
// quietly, since nobody will read its output.
var errParentGone = errors.New("parent session gone")

// parentWatcher snapshots the waiter's parent (the shell Claude runs
// background commands in) and grandparent (Claude itself). The session is
// gone when the waiter is reparented or the grandparent dies.
func parentWatcher() func() bool {
	ppid := os.Getppid()
	gp := parentPID(ppid)
	return func() bool {
		return os.Getppid() != ppid || (gp > 1 && !processAlive(gp))
	}
}

func watchParent(ctx context.Context, cancel context.CancelCauseFunc, gone func() bool, every time.Duration) {
	if every <= 0 {
		every = 2 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if gone() {
				cancel(errParentGone)
				return
			}
		}
	}
}

// parentPID returns pid's parent (0 if unknown): /proc on Linux, ps elsewhere.
func parentPID(pid int) int {
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		// Fields after the ")" that closes the command name: state ppid ...
		if i := strings.LastIndexByte(string(b), ')'); i >= 0 {
			if f := strings.Fields(string(b[i+1:])); len(f) > 1 {
				n, _ := strconv.Atoi(f[1])
				return n
			}
		}
	}
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}
