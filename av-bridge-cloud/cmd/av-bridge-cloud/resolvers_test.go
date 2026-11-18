package main

import (
	"testing"

	"github.com/dloomes/av-bridge-cloud/internal/config"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

func TestStaticTokenAllowed(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{"prod: token but no bootstrap", config.Config{PoCPortalToken: "tok"}, false},
		{"uat: bootstrap and token", config.Config{BootstrapPoC: true, PoCPortalToken: "tok"}, true},
		{"bootstrap, no token", config.Config{BootstrapPoC: true}, false},
		{"neither", config.Config{}, false},
	}
	for _, c := range cases {
		if got := staticTokenAllowed(c.cfg); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestPortalResolversStaticToken(t *testing.T) {
	chain := func(cfg config.Config) portalauth.Resolver {
		return portalauth.NewChainResolver(withStaticResolver(nil, cfg)...)
	}
	off := config.Config{PoCPortalToken: "tok", PoCPortalCustomerID: "c1", PoCPortalRole: "admin"}
	if _, ok := chain(off).Resolve("tok"); ok {
		t.Fatal("static token accepted with BOOTSTRAP_POC=false")
	}
	on := off
	on.BootstrapPoC = true
	if _, ok := chain(on).Resolve("tok"); !ok {
		t.Fatal("static token rejected with BOOTSTRAP_POC=true")
	}
}
