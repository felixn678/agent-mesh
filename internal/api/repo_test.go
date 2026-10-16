package api_test

import (
	"testing"

	"github.com/felixn678/agent-mesh/internal/api"
)

func TestNormalizeRepo(t *testing.T) {
	const want = "github.com/owner/name"
	ok := []string{
		"github.com/owner/name",
		"https://github.com/owner/name.git",
		"https://github.com/owner/name/",
		"HTTPS://GitHub.com/Owner/Name",
		"https://user:pw@github.com/owner/name.git",
		"http://github.com/owner/name",
		"git@github.com:owner/name.git",
		"git@github.com:Owner/Name",
		"github.com:owner/name",
		"ssh://git@github.com/owner/name.git",
		"ssh://git@github.com:22/owner/name.git",
		"git+ssh://git@github.com/owner/name",
		"git://github.com/owner/name.git",
		"  https://github.com/owner/name.git\n",
	}
	for _, in := range ok {
		if got, err := api.NormalizeRepo(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v; want %q", in, got, err, want)
		}
	}
	if got, err := api.NormalizeRepo("git@gitlab.example.org:grp/sub/proj.git"); err != nil || got != "gitlab.example.org/grp/sub/proj" {
		t.Errorf("subgroup: %q %v", got, err)
	}
	bad := []string{"", "name", "owner/name", "github.com/owner", "https://github.com/", "github.com/owner/../x",
		"github.com/owner/na me", "https://github.com/owner/name?x=1", "https://github.com/owner/name#frag", "a/b/c"}
	for _, in := range bad {
		if got, err := api.NormalizeRepo(in); err == nil || !api.IsValidation(err) {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

func TestCanonicalRepoLegacyKeysAndRemotes(t *testing.T) {
	for in, want := range map[string]string{
		"lumio":                             "lumio",
		"https://github.com/Owner/Name.git": "github.com/owner/name",
	} {
		if got, err := api.CanonicalRepo(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v", in, got, err)
		}
	}
	if _, err := api.CanonicalRepo("bad key!"); err == nil {
		t.Error("bad legacy key accepted")
	}
}

func TestTargetingValidation(t *testing.T) {
	base := api.TaskSpec{Repo: "r", Prompt: "p", MaxBudgetUSD: 1}
	good := base
	good.TargetHost, good.TargetSession, good.CreatedBySession = "host-a", "sess-1", "sess-2"
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mut := range []func(*api.TaskSpec){
		func(s *api.TaskSpec) { s.TargetHost = "Bad Host" },
		func(s *api.TaskSpec) { s.TargetSession = "a b" },
		func(s *api.TaskSpec) { s.CreatedBySession = "../x" },
	} {
		s := base
		mut(&s)
		if err := s.Validate(); !api.IsValidation(err) {
			t.Errorf("%+v: %v", s, err)
		}
	}
}
