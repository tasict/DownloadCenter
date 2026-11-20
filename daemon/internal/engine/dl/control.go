package dl

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/safefs"
)

// control is the progress of a download, kept as .<name>.dcdl next to the
// data in the task's own folder.
type control struct {
	V        int    `json:"v"`
	Ref      string `json:"ref"`
	URL      string `json:"url"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Piece    int64  `json:"piece"`
	Bits     string `json:"bits"`
	ETag     string `json:"etag,omitempty"`
	Modified string `json:"modified,omitempty"`
}

const controlSuffix = ".dcdl"

// IsControlFile reports whether a file name is an engine control file
// (the URL engine's own, or one left by aria2).
func IsControlFile(name string) bool {
	return strings.HasPrefix(name, ".") && strings.HasSuffix(name, controlSuffix) || strings.HasSuffix(name, ".aria2")
}

func controlPath(dir, name string) string {
	return filepath.Join(dir, controlName(name))
}

func controlName(name string) string { return "." + name + controlSuffix }

// Limits for what is read back from the task's folder.
const (
	maxControlFile = 1 << 20  // our control file (the bitfield of at most maxPieces pieces, in hex)
	maxAria2File   = 16 << 20 // an aria2 control file (its bitfield and in-flight pieces)
	minPiece       = 1 << 10
	maxPiece       = 1 << 30
	maxSavedPieces = 1 << 24
)

// saveControl writes the progress when the download can be resumed. It is
// written beside the data under a temporary name, then renamed: neither
// name is followed if it is a link.
func (t *task) saveControl() {
	t.mu.Lock()
	t.saved = time.Now()
	if !t.known || !t.ranges || t.size <= 0 || t.name == "" || t.state == engine.Complete {
		t.mu.Unlock()
		return
	}
	c := control{V: 1, Ref: t.ref, URL: t.uris[0], Name: t.name, Size: t.size, Piece: t.pieceLen,
		Bits: t.bitfield(), ETag: t.etag, Modified: t.modified}
	dir := t.req.Dir
	t.mu.Unlock()
	b, _ := json.Marshal(c)
	d, err := openWorkDir(dir)
	if err != nil {
		return
	}
	defer d.Close()
	name := controlName(c.Name)
	tmp := name + ".tmp"
	d.Remove(tmp)
	f, err := d.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil || d.Rename(tmp, d, name) != nil {
		d.Remove(tmp)
	}
}

// load picks up the progress of an earlier run: our control file first,
// then an aria2 control file from before the engine change. Only regular
// files are read, never through a link, and only up to a size.
func (t *task) load() {
	d, err := openWorkDir(t.req.Dir)
	if err != nil {
		return
	}
	defer d.Close()
	names, err := d.Names()
	if err != nil {
		return
	}
	for _, n := range names {
		if !strings.HasPrefix(n, ".") || !strings.HasSuffix(n, controlSuffix) {
			continue
		}
		b, err := d.ReadFile(n, maxControlFile)
		if err != nil {
			continue
		}
		var c control
		if json.Unmarshal(b, &c) != nil || c.Ref != t.ref || c.Name == "" || controlName(c.Name) != n {
			continue
		}
		bits, err := hex.DecodeString(c.Bits)
		if err != nil {
			continue
		}
		if t.adopt(d, c.Name, c.Size, c.Piece, bits) {
			t.mu.Lock()
			t.etag, t.modified = c.ETag, c.Modified
			t.mu.Unlock()
			return
		}
	}
	if t.loadAria2(d, names) {
		return
	}
	// No progress record: a data file under the requested name is continued
	// after its current size when the server allows ranges
	if t.req.Out != "" && !strings.ContainsAny(t.req.Out, "/\\") {
		if st, err := d.Lstat(t.req.Out); err == nil && st.Mode&syscall.S_IFMT == syscall.S_IFREG && st.Nlink == 1 &&
			st.Size > 0 && st.Size <= maxFileSize {
			t.mu.Lock()
			t.contSize = st.Size
			t.mu.Unlock()
		}
	}
}

// loadAria2 takes over the progress of an aria2 control file.
func (t *task) loadAria2(d *safefs.Dir, names []string) bool {
	for _, n := range names {
		if !strings.HasSuffix(n, ".aria2") {
			continue
		}
		b, err := d.ReadFile(n, maxAria2File)
		if err != nil {
			continue
		}
		piece, total, bits, err := parseAria2Control(b)
		if err != nil {
			continue
		}
		if t.adopt(d, strings.TrimSuffix(n, ".aria2"), total, piece, bits) {
			t.mu.Lock()
			t.adopted = n
			t.mu.Unlock()
			t.e.logf(t.ref, "Continuing from the progress aria2 had saved")
			return true
		}
	}
	return false
}

// adopt takes over saved progress when the data file is there and the
// numbers add up.
func (t *task) adopt(d *safefs.Dir, name string, size, piece int64, bits []byte) bool {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return false
	}
	if size <= 0 || size > maxFileSize || piece < minPiece || piece > maxPiece {
		return false
	}
	n := pieceCount(size, piece)
	if n > maxSavedPieces || int64(len(bits)) != (n+7)/8 {
		return false
	}
	st, err := d.Lstat(name)
	if err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 {
		return false
	}
	done := make([]bool, n)
	for i := range done {
		done[i] = bits[i/8]&(0x80>>uint(i%8)) != 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.known, t.name, t.size, t.ranges, t.pieceLen, t.done = true, name, size, true, piece, done
	return true
}

// removeAdopted deletes the aria2 control file once our own is written.
func (t *task) removeAdopted() {
	t.mu.Lock()
	n := t.adopted
	t.adopted = ""
	d := t.dir
	t.mu.Unlock()
	if n != "" && d != nil {
		d.Remove(n)
	}
}

// parseAria2Control reads the piece length, total length and bitfield of
// an aria2 control file ("Control File (*.aria2) Format" in the aria2
// manual): version 1 is big-endian, version 0 host order (little-endian on
// QNAP's CPUs). Lengths are checked as 64-bit numbers, on every CPU.
func parseAria2Control(b []byte) (piece, total int64, bits []byte, err error) {
	bad := errors.New("not an aria2 control file")
	if len(b) < 10 {
		return 0, 0, nil, bad
	}
	var bo binary.ByteOrder
	switch binary.BigEndian.Uint16(b) {
	case 1:
		bo = binary.BigEndian
	case 0:
		bo = binary.LittleEndian
	default:
		return 0, 0, nil, bad
	}
	p := int64(6) // version, extension
	size := int64(len(b))
	u32 := func() (int64, bool) {
		if p+4 > size {
			return 0, false
		}
		v := int64(bo.Uint32(b[p:]))
		p += 4
		return v, true
	}
	ihLen, ok := u32()
	if !ok || ihLen != 0 {
		// Torrents have an info hash; only plain downloads are taken over
		return 0, 0, nil, bad
	}
	if piece, ok = u32(); !ok || piece <= 0 {
		return 0, 0, nil, bad
	}
	if p+16 > size {
		return 0, 0, nil, bad
	}
	total = int64(bo.Uint64(b[p:]))
	p += 16 // total length, upload length
	bfLen, ok := u32()
	if !ok || total <= 0 || total > maxFileSize || bfLen > size-p {
		return 0, 0, nil, bad
	}
	return piece, total, b[p : p+bfLen], nil
}
