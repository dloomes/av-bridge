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

func setMinimalEnv(t *testing.T) {
	t.Setenv("DATABASE_MIGRATION_URL", "postgres://x")
	t.Setenv("DATABASE_ADMIN_URL", "postgres://x")
	t.Setenv("DATABASE_TENANT_URL", "postgres://x")
	t.Setenv("CLOUD_SECRET_KEY", "0000000000000000000000000000000000000000000000000000000000000000")
	t.Setenv("BOOTSTRAP_POC", "false")
}

func TestFromEnv_DBAppPasswords(t *testing.T) {
	long1 := "aaaaaaaaaaaaaaaaaaaaaaaa"
	long2 := "bbbbbbbbbbbbbbbbbbbbbbbb"
	cases := []struct {
		name, admin, tenant string
		wantErr             bool
	}{
		{"neither", "", "", false},
		{"both", long1, long2, false},
		{"only admin", long1, "", true},
		{"only tenant", "", long2, true},
		{"short admin", "short", long2, true},
		{"short tenant", long1, "short", true},
	}
	for _, c := range cases {
		setMinimalEnv(t)
		t.Setenv("DB_APP_ADMIN_PASSWORD", c.admin)
		t.Setenv("DB_APP_TENANT_PASSWORD", c.tenant)
		cfg, err := FromEnv()
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v wantErr=%v", c.name, err != nil, c.wantErr)
		}
		if err == nil && (cfg.DBAppAdminPassword != c.admin || cfg.DBAppTenantPassword != c.tenant) {
			t.Errorf("%s: passwords not carried into Config", c.name)
		}
	}
}
