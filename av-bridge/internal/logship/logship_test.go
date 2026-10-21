package logship

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestHandlerCapturesInfoAndAbove(t *testing.T) {
	var out bytes.Buffer
	buf := &Buffer{}
	log := slog.New(NewHandler(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelWarn}), buf))

	log.Debug("not sent")
	log.Info("device connected", "device", "dsp-1", "user", "admin", "password", "hunter2")
	log.With("device", "pdu-1").WithGroup("http").Warn("slow", "ms", 900, "api_token", "abc")

	recs := buf.Take()
	if len(recs) != 2 {
		t.Fatalf("records = %+v, want Info and Warn only", recs)
	}
	if r := recs[0]; r.Level != "INFO" || r.Msg != "device connected" || r.Attrs["device"] != "dsp-1" ||
		r.Attrs["user"] != "a***" || r.Attrs["password"] != "[removed]" {
		t.Errorf("info record = %+v", r)
	}
	if r := recs[1]; r.Level != "WARN" || r.Attrs["device"] != "pdu-1" || r.Attrs["http.ms"] != int64(900) ||
		r.Attrs["http.api_token"] != "[removed]" {
		t.Errorf("warn record = %+v", r)
	}
	// The local log keeps its own level (Warn here) and its own content.
	if strings.Contains(out.String(), "device connected") || !strings.Contains(out.String(), "slow") {
		t.Errorf("local log = %s", out.String())
	}
	if buf.Take() != nil {
		t.Error("Take should empty the buffer")
	}
}

func TestBufferDropsOldestAndSaysSo(t *testing.T) {
	buf := &Buffer{}
	for i := 0; i < maxBuffered+10; i++ {
		buf.add(Record{Msg: "line"})
	}
	recs := buf.Take()
	if len(recs) != maxBuffered+1 {
		t.Fatalf("len = %d, want %d (note + buffer)", len(recs), maxBuffered+1)
	}
	if !strings.Contains(recs[0].Msg, "10 log lines were not sent") {
		t.Errorf("first record = %+v", recs[0])
	}
}

func TestLongValuesAreTruncated(t *testing.T) {
	buf := &Buffer{}
	slog.New(NewHandler(slog.NewJSONHandler(&bytes.Buffer{}, nil), buf)).
		Info(strings.Repeat("m", maxMsgLen+50), "body", strings.Repeat("b", maxAttrLen+50))
	r := buf.Take()[0]
	if len(r.Msg) > maxMsgLen+4 || len(r.Attrs["body"].(string)) > maxAttrLen+4 {
		t.Errorf("not truncated: msg %d, body %d", len(r.Msg), len(r.Attrs["body"].(string)))
	}
}

func TestRotatingFile(t *testing.T) {
	path := t.TempDir() + "/logs/av-bridge.log"
	f, err := OpenRotatingFile(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes
	for i := 0; i < 10; i++ {
		if _, err := f.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{path, path + ".1", path + ".2"} {
		info, err := os.Stat(p)
		if err != nil || info.Size() == 0 || info.Size() > 100 {
			t.Errorf("%s: %v size %v", p, err, info)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("kept more than 2 old files")
	}
}
