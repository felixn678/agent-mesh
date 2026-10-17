package api

import (
	"regexp"
	"unicode/utf8"
)

// Host profiles tell sessions what each machine is for, so a session can pick
// target_host itself when the user does not name a machine. The note is
// written by the user; OS and tools are detected by the session-start hook.

// Host profile limits.
const (
	MaxHostNoteBytes = 300
	MaxMachineTools  = 30
)

var toolNameRE = regexp.MustCompile(`^[a-z0-9._+-]{1,32}$`)

// MachineInfo is what a session-start hook detects about its machine.
type MachineInfo struct {
	OS    string   `json:"os,omitempty"`    // e.g. "macos/arm64"
	Tools []string `json:"tools,omitempty"` // e.g. ["xcode","adb","node"]
}

// Validate bounds the detected values (they are shown to other sessions).
func (m MachineInfo) Validate() error {
	if len(m.OS) > 50 || !utf8.ValidString(m.OS) {
		return invalid("os must be valid UTF-8 of at most 50 bytes")
	}
	if len(m.Tools) > MaxMachineTools {
		return invalid("tools must list at most %d entries", MaxMachineTools)
	}
	for _, t := range m.Tools {
		if !toolNameRE.MatchString(t) {
			return invalid("tool names must match %s", toolNameRE)
		}
	}
	return nil
}

// HostNoteRequest sets (or, empty, clears) a machine's role note.
type HostNoteRequest struct {
	Note string `json:"note"`
}

// Validate bounds the note.
func (r HostNoteRequest) Validate() error {
	if len(r.Note) > MaxHostNoteBytes || !utf8.ValidString(r.Note) {
		return invalid("note must be valid UTF-8 of at most %d bytes", MaxHostNoteBytes)
	}
	return nil
}

// HostProfile is one machine as sessions see it: the user's role note, the
// detected OS and tools, and how many Claude sessions are live there.
type HostProfile struct {
	Name         string   `json:"name"`
	Note         string   `json:"note,omitempty"`
	OS           string   `json:"os,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	LiveSessions int      `json:"live_sessions"`
	UpdatedAt    int64    `json:"updated_at,omitempty"`
}
