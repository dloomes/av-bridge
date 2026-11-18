package brandurl

import "testing"

func TestOrigin(t *testing.T) {
	cases := []struct{ base, domain, slug, want string }{
		{"https://app.uat.involvecloud.com", "", "acme", "https://acme.uat.involvecloud.com"},
		{"https://app.involvecloud.com", "app.involvecloud.com", "acme", "https://acme.app.involvecloud.com"},
		{"https://app.involvecloud.com/", "app.involvecloud.com", "", "https://app.involvecloud.com"},
		{"", "x", "acme", ""},
		{"https://localhost:3000", "", "acme", "https://localhost:3000"},
	}
	for _, c := range cases {
		if got := Origin(c.base, c.domain, c.slug); got != c.want {
			t.Errorf("Origin(%q,%q,%q)=%q want %q", c.base, c.domain, c.slug, got, c.want)
		}
	}
}
