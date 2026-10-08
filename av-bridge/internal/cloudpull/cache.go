package cloudpull

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/dloomes/av-bridge/internal/config"
)

// The device cache keeps the last device set pulled from the cloud on
// disk, so a bridge that restarts while the cloud is unreachable carries
// on polling the devices the cloud last gave it — not the (possibly long
// out of date) devices in its local YAML.
//
// The set includes device credentials, so the file is sealed with
// AES-256-GCM under a key derived from the collector's HMAC secret. If the
// secret changes (re-enrolment), the old cache simply fails to open and
// is ignored until the next successful pull rewrites it.

const cacheKeyContext = "av-bridge device cache v1\x00"

func cacheKey(hmacSecret string) []byte {
	k := sha256.Sum256([]byte(cacheKeyContext + hmacSecret))
	return k[:]
}

// cacheFile is the sealed content. HA marks a warm-standby group member,
// which must not start polling from the cache (see LoadCache).
type cacheFile struct {
	HA      bool         `json:"ha,omitempty"`
	Devices []wireDevice `json:"devices"`
}

// saveCache writes devices to path atomically (temp file + rename).
func saveCache(path, hmacSecret string, devices []wireDevice, ha bool) error {
	plain, err := json.Marshal(cacheFile{HA: ha, Devices: devices})
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(cacheKey(hmacSecret))
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := gcm.Seal(nonce, nonce, plain, nil)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, sealed, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// LoadCache returns the device set last pulled from the cloud. ok is false
// when there is no usable cache (missing, from another secret, corrupt),
// and also for a warm-standby group member: it must wait for the cloud to
// confirm it holds the lease before polling anything, or a restart could
// put two machines on the same devices.
func LoadCache(path, hmacSecret string) (devices []config.DeviceConfig, ok bool, err error) {
	if path == "" || hmacSecret == "" {
		return nil, false, nil
	}
	sealed, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	block, err := aes.NewCipher(cacheKey(hmacSecret))
	if err != nil {
		return nil, false, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, false, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, false, fmt.Errorf("device cache truncated")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	if err != nil {
		return nil, false, fmt.Errorf("device cache unreadable (secret changed?): %w", err)
	}
	var cf cacheFile
	if err := json.Unmarshal(plain, &cf); err != nil {
		// Caches written before group support are a bare array.
		if err2 := json.Unmarshal(plain, &cf.Devices); err2 != nil {
			return nil, false, fmt.Errorf("device cache corrupt: %w", err)
		}
	}
	if cf.HA {
		return nil, false, nil
	}
	wire := cf.Devices
	out := make([]config.DeviceConfig, len(wire))
	for i, d := range wire {
		out[i] = d.toConfig()
	}
	return out, true, nil
}
