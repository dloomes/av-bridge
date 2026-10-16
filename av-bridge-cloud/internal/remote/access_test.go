package remote

import "testing"

func TestCleanStartPath(t *testing.T) {
	ok := map[string]string{
		"":                      "/",
		"   ":                   "/",
		"/":                     "/",
		"/login":                "/login",
		"login":                 "/login",
		"index.html":            "/index.html",
		"/cgi-bin/main?page=1":  "/cgi-bin/main?page=1",
		"/web/#":                "",
		"//evil.example/login":  "",
		`/\evil.example`:        "",
		"https://evil.example/": "",
		"/a b":                  "",
		"/a\nb":                 "",
		launchPath + "?code=x":  "",
	}
	for in, want := range ok {
		got, err := CleanStartPath(in)
		if want == "" {
			if err == nil {
				t.Errorf("CleanStartPath(%q) = %q, want refused", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("CleanStartPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
