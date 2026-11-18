package db

import "testing"

func TestPoolConfigMaxConns(t *testing.T) {
	cases := []struct {
		dsn  string
		want int32
	}{
		{"postgres://u:p@localhost:5432/d?sslmode=disable", 20},
		{"postgres://u:p@localhost:5432/d?sslmode=disable&pool_max_conns=7", 7},
	}
	for _, c := range cases {
		cfg, err := poolConfig(c.dsn)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MaxConns != c.want {
			t.Errorf("%s: MaxConns = %d, want %d", c.dsn, cfg.MaxConns, c.want)
		}
	}
}
