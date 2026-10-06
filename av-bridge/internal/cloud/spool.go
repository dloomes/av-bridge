package cloud

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// spool is the bridge's store-and-forward buffer: ingest payloads the cloud
// couldn't accept are written here, one file per payload, and replayed
// oldest-first once the cloud is reachable again. Files survive restarts,
// so an outage (cloud, internet, or the bridge host rebooting mid-outage)
// doesn't lose telemetry.
//
// Bounded two ways so a long outage can't fill the disk: files older than
// maxAge are discarded, and the oldest files go first when the total
// exceeds maxBytes.
//
// File names are "<unix-nanos>-<seq>.json", so lexical order is arrival
// order and the age is readable without a stat.
type spool struct {
	dir      string
	maxBytes int64
	maxAge   time.Duration

	mu  sync.Mutex
	seq uint64
}

type spoolEntry struct {
	name string
	size int64
	at   time.Time
}

func newSpool(dir string, maxBytes int64, maxAge time.Duration) (*spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create spool dir: %w", err)
	}
	// Leftover temp files are from a crash mid-write; they were never
	// renamed into the queue, so they're safe to drop.
	if tmps, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(tmps) > 0 {
		for _, t := range tmps {
			_ = os.Remove(t)
		}
	}
	return &spool{dir: dir, maxBytes: maxBytes, maxAge: maxAge}, nil
}

// put appends one payload to the queue, then enforces the bounds.
func (s *spool) put(body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	name := fmt.Sprintf("%020d-%06d.json", time.Now().UnixNano(), s.seq%1_000_000)
	tmp := filepath.Join(s.dir, name+".tmp")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	s.trimLocked()
	return nil
}

// entries lists queued payloads oldest-first, after dropping expired ones.
func (s *spool) entries() []spoolEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trimLocked()
	return s.listLocked()
}

func (s *spool) read(e spoolEntry) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.dir, e.name))
}

func (s *spool) remove(e spoolEntry) {
	_ = os.Remove(filepath.Join(s.dir, e.name))
}

func (s *spool) empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.listLocked()) == 0
}

func (s *spool) listLocked() []spoolEntry {
	des, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	out := make([]spoolEntry, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		nanos, err := strconv.ParseInt(strings.SplitN(name, "-", 2)[0], 10, 64)
		if err != nil {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, spoolEntry{name: name, size: info.Size(), at: time.Unix(0, nanos)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// trimLocked drops expired files, then the oldest until under maxBytes.
// Data loss here is the deliberate bound, so it's logged loudly.
func (s *spool) trimLocked() {
	dropped := 0
	defer func() {
		if dropped > 0 {
			slog.Error("cloud spool over its limits, oldest data discarded",
				"dropped_payloads", dropped, "max_age", s.maxAge, "max_bytes", s.maxBytes)
		}
	}()
	es := s.listLocked()
	var total int64
	keep := es[:0]
	for _, e := range es {
		if s.maxAge > 0 && time.Since(e.at) > s.maxAge {
			_ = os.Remove(filepath.Join(s.dir, e.name))
			dropped++
			continue
		}
		total += e.size
		keep = append(keep, e)
	}
	for i := 0; s.maxBytes > 0 && total > s.maxBytes && i < len(keep); i++ {
		_ = os.Remove(filepath.Join(s.dir, keep[i].name))
		total -= keep[i].size
		dropped++
	}
}
