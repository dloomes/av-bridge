package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadEnvFile reads KEY=VALUE pairs from path and sets them in the process
// environment. The file format is the same one bash's `source` understands
// for simple assignments:
//
//	# comments and blank lines are ignored
//	KEY=value
//	KEY="quoted value"
//	export KEY=value
//
// Existing OS environment variables take precedence — they are NOT overwritten —
// so an operator can still override a single value on the command line.
//
// Returns nil (no error) if the file does not exist, so callers can probe for
// optional defaults without checking os.IsNotExist themselves.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open env file %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		s := strings.TrimSpace(scanner.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")

		eq := strings.Index(s, "=")
		if eq < 0 {
			return fmt.Errorf("env file %s line %d: missing '='", path, line)
		}

		key := strings.TrimSpace(s[:eq])
		val := strings.TrimSpace(s[eq+1:])
		if key == "" {
			return fmt.Errorf("env file %s line %d: empty key", path, line)
		}

		if len(val) >= 2 {
			first, last := val[0], val[len(val)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				val = val[1 : len(val)-1]
			}
		}

		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return fmt.Errorf("setenv %s: %w", key, err)
		}
	}
	return scanner.Err()
}
