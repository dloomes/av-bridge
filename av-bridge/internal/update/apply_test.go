package update

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The test binary doubles as a fake "new collector": with this env var
// set it answers -version and -validate the way av-bridge does.
const fakeBridgeEnv = "AV_BRIDGE_UPDATE_TEST_FAKE"

func TestMain(m *testing.M) {
	if v := os.Getenv(fakeBridgeEnv); v != "" {
		for _, a := range os.Args[1:] {
			switch a {
			case "-version":
				fmt.Printf("av-bridge version %s\n", v)
				os.Exit(0)
			case "-validate":
				fmt.Println("config valid")
				os.Exit(0)
			}
		}
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestApplyInstallsAndRestarts(t *testing.T) {
	priv := testKey(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/public/downloads/av-bridge-test" {
			http.NotFound(w, r)
			return
		}
		w.Write(payload)
	}))
	defer srv.Close()
	t.Setenv(fakeBridgeEnv, "v1.1.0")

	u, rec, restarts, exe := newTestUpdater(t, srv.URL)
	sha := SHA256Hex(payload)
	u.Apply(context.Background(), Instruction{
		Version: "v1.1.0", Artefact: "av-bridge-test", URL: "/public/downloads/av-bridge-test",
		SHA256: sha, Signature: Sign(priv, "av-bridge-test", "v1.1.0", sha),
	})

	if s, m := rec.last(); s != StateRestarting {
		t.Fatalf("want restarting, got %s %q", s, m)
	}
	if *restarts != 1 {
		t.Fatalf("want one restart request, got %d", *restarts)
	}
	got, _ := os.ReadFile(exe)
	if !bytes.Equal(got, payload) {
		t.Fatal("live binary should be the downloaded one")
	}
	if prev, _ := os.ReadFile(exe + ".prev"); string(prev) != "old" {
		t.Fatal("previous binary should be kept as .prev for rollback")
	}
	m, ok := readMarker(filepath.Dir(exe))
	if !ok || m.From != "v1.0.0" || m.To != "v1.1.0" {
		t.Fatalf("pending marker wrong: %+v %v", m, ok)
	}

	// The new binary claiming the wrong version is rejected.
	t.Setenv(fakeBridgeEnv, "v6.6.6")
	u2, rec2, restarts2, _ := newTestUpdater(t, srv.URL)
	u2.Apply(context.Background(), Instruction{
		Version: "v1.1.0", Artefact: "av-bridge-test", URL: "/public/downloads/av-bridge-test",
		SHA256: sha, Signature: Sign(priv, "av-bridge-test", "v1.1.0", sha),
	})
	if s, _ := rec2.last(); s != StateFailed || *restarts2 != 0 {
		t.Fatalf("version mismatch must fail without restart, got %s restarts=%d", s, *restarts2)
	}
}
