package notify

import (
	"strings"
	"testing"
)

// The email preheader leads with the severity as a capitalised word,
// whatever case it is stored in.
func TestAlertPreheaderCapitalisesSeverity(t *testing.T) {
	for in, want := range map[string]string{
		"critical": "Critical alert on ",
		"WARNING":  "Warning alert on ",
		"Info":     "Info alert on ",
	} {
		got := renderAlertHTML(AlertEvent{Severity: in})
		if !strings.Contains(got, want) {
			t.Errorf("severity %q: preheader missing %q", in, want)
		}
	}
}

func TestTitleWordsMatchesStringsTitle(t *testing.T) {
	for in, want := range map[string]string{
		"critical": "Critical", "warning": "Warning", "info": "Info",
		"": "", "two words": "Two Words", "élan": "Élan",
	} {
		if got := titleWords(in); got != want {
			t.Errorf("titleWords(%q) = %q, want %q", in, got, want)
		}
	}
}
