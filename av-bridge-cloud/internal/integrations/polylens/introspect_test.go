//go:build lensverify

package polylens

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLensIntrospect prints schema type/field/argument names only.
func TestLensIntrospect(t *testing.T) {
	c := newVerifyClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	type tref struct {
		Name   *string `json:"name"`
		Kind   string  `json:"kind"`
		OfType *struct {
			Name   *string `json:"name"`
			Kind   string  `json:"kind"`
			OfType *struct {
				Name *string `json:"name"`
			} `json:"ofType"`
		} `json:"ofType"`
	}
	show := func(r tref) string {
		s := ""
		if r.Name != nil {
			s = *r.Name
		}
		if r.OfType != nil {
			if r.OfType.Name != nil {
				s = *r.OfType.Name
			} else if r.OfType.OfType != nil && r.OfType.OfType.Name != nil {
				s = "[" + *r.OfType.OfType.Name + "]"
			}
			s = r.OfType.Kind + ":" + s
		}
		return s
	}
	for _, tn := range strings.Split(os.Getenv("LENS_TYPES"), ",") {
		var out struct {
			Type *struct {
				Kind   string `json:"kind"`
				Fields []struct {
					Name string `json:"name"`
					Type tref   `json:"type"`
					Args []struct {
						Name string `json:"name"`
						Type tref   `json:"type"`
					} `json:"args"`
				} `json:"fields"`
				InputFields []struct {
					Name string `json:"name"`
					Type tref   `json:"type"`
				} `json:"inputFields"`
				EnumValues []struct {
					Name string `json:"name"`
				} `json:"enumValues"`
			} `json:"__type"`
		}
		q := `query($n:String!){ __type(name:$n){ kind
 fields{ name type{name kind ofType{name kind ofType{name}}} args{ name type{name kind ofType{name kind ofType{name}}} } }
 inputFields{ name type{name kind ofType{name kind ofType{name}}} }
 enumValues{ name } } }`
		if err := c.graphql(ctx, q, map[string]any{"n": tn}, &out); err != nil {
			t.Logf("%s: error %v", tn, err)
			continue
		}
		if out.Type == nil {
			t.Logf("%s: not found", tn)
			continue
		}
		t.Logf("== %s (%s)", tn, out.Type.Kind)
		for _, f := range out.Type.Fields {
			a := []string{}
			for _, x := range f.Args {
				a = append(a, x.Name+":"+show(x.Type))
			}
			t.Logf("  %s: %s (%s)", f.Name, show(f.Type), strings.Join(a, ", "))
		}
		for _, f := range out.Type.InputFields {
			t.Logf("  in %s: %s", f.Name, show(f.Type))
		}
		for _, e := range out.Type.EnumValues {
			t.Logf("  enum %s", e.Name)
		}
	}
}
