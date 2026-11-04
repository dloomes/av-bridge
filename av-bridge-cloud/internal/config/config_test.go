package config

import "testing"

func TestFromEnv_AllowMockJWT(t *testing.T) {
	t.Setenv("DATABASE_MIGRATION_URL", "postgres://x")
	t.Setenv("DATABASE_ADMIN_URL", "postgres://x")
	t.Setenv("DATABASE_TENANT_URL", "postgres://x")
	t.Setenv("CLOUD_SECRET_KEY", "0000000000000000000000000000000000000000000000000000000000000000")
	t.Setenv("BOOTSTRAP_POC", "false")

	cases := []struct {
		val  string
		want bool
	}{{"", false}, {"true", true}, {"TRUE", false}, {"1", false}, {"yes", false}, {"false", false}}
	for _, c := range cases {
		t.Setenv("ALLOW_MOCK_JWT", c.val)
		cfg, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv: %v", err)
		}
		if cfg.AllowMockJWT != c.want {
			t.Errorf("ALLOW_MOCK_JWT=%q: got %v, want %v", c.val, cfg.AllowMockJWT, c.want)
		}
	}
}
