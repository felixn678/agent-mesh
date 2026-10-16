// Package soak holds the soak/chaos test of the real hub binary. The test is
// behind the "soak" build tag so the default `go test ./...` stays fast:
//
//	go test -tags soak -race -short ./internal/soak   # ~60 s
//	go test -tags soak -timeout 15m ./internal/soak   # full ~3 min
//
// SOAK_DURATION (e.g. "3m") overrides the chaos phase length.
package soak
