package config

import (
	"strings"
	"testing"
)

func TestParseRole(t *testing.T) {
	for in, want := range map[string]Role{"": RoleAll, "all": RoleAll, "WEB": RoleWeb, "worker": RoleWorker, " Worker ": RoleWorker} {
		got, err := ParseRole(in)
		if err != nil || got != want {
			t.Errorf("ParseRole(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	_, err := ParseRole("both")
	if err == nil || !strings.Contains(err.Error(), "all, web, worker") {
		t.Errorf("ParseRole(both) err = %v; want one naming all, web, worker", err)
	}
}

func TestRoleCapabilities(t *testing.T) {
	cases := []struct {
		r           Role
		bg, tunnels bool
	}{{RoleWeb, false, false}, {RoleWorker, true, true}, {RoleAll, true, true}}
	for _, c := range cases {
		if c.r.RunsBackground() != c.bg || c.r.ServesTunnels() != c.tunnels {
			t.Errorf("%s: RunsBackground=%v ServesTunnels=%v", c.r, c.r.RunsBackground(), c.r.ServesTunnels())
		}
	}
}

func TestFromEnvRole(t *testing.T) {
	t.Setenv("MARCUS_ROLE", "bogus")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "MARCUS_ROLE") {
		t.Errorf("FromEnv err = %v; want one naming MARCUS_ROLE", err)
	}
}
