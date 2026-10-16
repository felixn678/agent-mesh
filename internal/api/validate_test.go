package api

import (
	"math"
	"strings"
	"testing"
)

func goodSpec() TaskSpec {
	return TaskSpec{Repo: "agent-mesh", Prompt: "do it", MaxBudgetUSD: 5}
}

func TestTaskSpecValidate(t *testing.T) {
	if err := goodSpec().Validate(); err != nil {
		t.Fatalf("good spec rejected: %v", err)
	}
	cases := map[string]func(*TaskSpec){
		"bypassPermissions": func(s *TaskSpec) { s.PermissionMode = "bypassPermissions" },
		"auto":              func(s *TaskSpec) { s.PermissionMode = "auto" },
		"manual":            func(s *TaskSpec) { s.PermissionMode = "manual" },
		"dash base":         func(s *TaskSpec) { s.BaseBranch = "-x" },
		"agentmesh base":    func(s *TaskSpec) { s.BaseBranch = "agentmesh/task-1-f1" },
		"dotdot base":       func(s *TaskSpec) { s.BaseBranch = "a..b" },
		"lock base":         func(s *TaskSpec) { s.BaseBranch = "x.lock" },
		"double slash":      func(s *TaskSpec) { s.BaseBranch = "a//b" },
		"trailing slash":    func(s *TaskSpec) { s.BaseBranch = "a/" },
		"space base":        func(s *TaskSpec) { s.BaseBranch = "a b" },
		"bad repo":          func(s *TaskSpec) { s.Repo = "../etc" },
		"empty repo":        func(s *TaskSpec) { s.Repo = "" },
		"upper host":        func(s *TaskSpec) { s.Host = "Box" },
		"bad profile":       func(s *TaskSpec) { s.ToolProfile = "Bash(rm *)" },
		"zero budget":       func(s *TaskSpec) { s.MaxBudgetUSD = 0 },
		"huge budget":       func(s *TaskSpec) { s.MaxBudgetUSD = 101 },
		"nan budget":        func(s *TaskSpec) { s.MaxBudgetUSD = math.NaN() },
		"blank prompt":      func(s *TaskSpec) { s.Prompt = "  \n" },
		"big prompt":        func(s *TaskSpec) { s.Prompt = strings.Repeat("a", MaxPromptBytes+1) },
		"attempts 0 neg":    func(s *TaskSpec) { s.MaxAttempts = -1 },
		"attempts 6":        func(s *TaskSpec) { s.MaxAttempts = 6 },
	}
	for name, mut := range cases {
		s := goodSpec()
		mut(&s)
		err := s.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if !IsValidation(err) {
			t.Errorf("%s: not a ValidationError: %v", name, err)
		}
	}
	for _, m := range []string{"acceptEdits", "dontAsk", "plan"} {
		s := goodSpec()
		s.PermissionMode = m
		if err := s.Validate(); err != nil {
			t.Errorf("mode %s rejected: %v", m, err)
		}
	}
}

func TestWithDefaults(t *testing.T) {
	s := goodSpec().WithDefaults()
	if s.BaseBranch != "main" || s.PermissionMode != "acceptEdits" || s.ToolProfile != "default" || s.MaxAttempts != 3 {
		t.Fatalf("unexpected defaults: %+v", s)
	}
}

func TestValidPRURL(t *testing.T) {
	ok := []string{"", "https://github.com/o/r/pull/12"}
	bad := []string{"http://github.com/o/r/pull/1", "https://evil.com/o/r/pull/1",
		"https://github.com/o/r/pull/x", "https://github.com/o/r/pull/1/files",
		"https://github.com/o/r/pull/1\n", "javascript:alert(1)"}
	for _, s := range ok {
		if !ValidPRURL(s) {
			t.Errorf("%q rejected", s)
		}
	}
	for _, s := range bad {
		if ValidPRURL(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestValidBranchAllowsAgentNamespace(t *testing.T) {
	if !ValidBranch("agentmesh/task-1-f1") {
		t.Fatal("worker branch must be a valid branch")
	}
	if ValidBaseBranch("agentmesh/task-1-f1") {
		t.Fatal("worker branch must not be a valid base")
	}
}

func TestRequestValidation(t *testing.T) {
	bad := []interface{ Validate() error }{
		CompleteRequest{Fence: 0},
		CompleteRequest{Fence: 1, PRURL: "https://example.com/x"},
		CompleteRequest{Fence: 1, Branch: "-x"},
		CompleteRequest{Fence: 1, CostUSD: -1},
		CompleteRequest{Fence: 1, CostUSD: math.Inf(1)},
		FailRequest{Fence: 1, Reason: strings.Repeat("x", 3000)},
		ReleaseRequest{Fence: 0},
		HeartbeatRequest{Fence: 1, Load: &HostLoad{CPUPct: 101}},
		ClaimRequest{BudgetCap: -1},
		ClaimRequest{Repos: make([]string, 101)},
	}
	for i, r := range bad {
		if r.Validate() == nil {
			t.Errorf("case %d accepted: %+v", i, r)
		}
	}
	good := []interface{ Validate() error }{
		CompleteRequest{Fence: 1, Branch: "agentmesh/task-1-f1", PRURL: "https://github.com/o/r/pull/3", CostUSD: 1.5},
		FailRequest{Fence: 2, Retry: true},
		ReleaseRequest{Fence: 2, Reason: "profile mismatch"},
		ClaimRequest{Repos: []string{"a"}, BudgetCap: 10},
	}
	for i, r := range good {
		if err := r.Validate(); err != nil {
			t.Errorf("good case %d: %v", i, err)
		}
	}
}
