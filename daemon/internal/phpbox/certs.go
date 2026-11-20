package phpbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/pem"
	"os"
	"path/filepath"
	"sync"

	"downloadcenter/internal/netutil"
)

// The certificate authorities PHP's curl trusts in the box: the system's
// and the package's CA list (netutil.ExtraRootsPEM), merged into one file
// that the box gets a copy of at boxCAFile. The system's directory stays
// in the box as well.

// systemCerts is the system's directory of certificate authorities (tests
// replace it).
var systemCerts = "/etc/ssl/certs"

const (
	boxCAFile  = "/host/cacert.pem"
	maxCAFile  = 8 << 20 // bytes of the merged file
	caFileName = "cacert.pem"
)

type caCache struct {
	mu   sync.Mutex
	sum  [32]byte // of what the file was made from
	path string
}

// caFile writes the merged file into the Runtime's directory when what it
// is made from changed, and returns its path ("" when there is nothing to
// trust besides the system's directory, or it cannot be written: then the
// box works as before, with the system's directory only).
func (r *Runtime) caFile() string {
	extra := netutil.ExtraRootsPEM()
	if len(extra) == 0 || r.cfg.Dir == "" {
		return ""
	}
	merged := mergeCerts(systemCerts, extra)
	sum := sha256.Sum256(merged)
	r.ca.mu.Lock()
	defer r.ca.mu.Unlock()
	path := filepath.Join(r.cfg.Dir, caFileName)
	if r.ca.path == path && r.ca.sum == sum && exists(path) {
		return path
	}
	if len(merged) == 0 || len(merged) > maxCAFile {
		return ""
	}
	if err := os.MkdirAll(r.cfg.Dir, 0o700); err != nil {
		return ""
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, merged, 0o600); err != nil || os.Rename(tmp, path) != nil {
		os.Remove(tmp)
		return ""
	}
	r.ca.sum, r.ca.path = sum, path
	return path
}

// mergeCerts is the certificates of the files in dir and of extra, each
// once, as PEM.
func mergeCerts(dir string, extra []byte) []byte {
	var out bytes.Buffer
	seen := map[[32]byte]bool{}
	add := func(b []byte) {
		for {
			var block *pem.Block
			if block, b = pem.Decode(b); block == nil {
				return
			}
			if block.Type != "CERTIFICATE" {
				continue
			}
			k := sha256.Sum256(block.Bytes)
			if seen[k] || out.Len() > maxCAFile {
				continue
			}
			seen[k] = true
			pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		// Regular files and links to them (the hashed names)
		p := filepath.Join(dir, e.Name())
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
			continue
		}
		if b, err := os.ReadFile(p); err == nil {
			add(b)
		}
	}
	add(extra)
	return out.Bytes()
}
