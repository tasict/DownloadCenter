package dl

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"downloadcenter/internal/engine"
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
	return filepath.Join(dir, "."+name+controlSuffix)
}

// saveControl writes the progress when the download can be resumed.
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
	p := controlPath(dir, c.Name)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return
	}
	if os.Rename(tmp, p) != nil {
		os.Remove(tmp)
	}
}

// load picks up the progress of an earlier run: our control file first,
// then an aria2 control file from before the engine change.
func (t *task) load() {
	dir := t.req.Dir
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, ".") || !strings.HasSuffix(n, controlSuffix) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		var c control
		if json.Unmarshal(b, &c) != nil || c.Ref != t.ref || c.Size <= 0 || c.Piece <= 0 || c.Name == "" || controlPath(dir, c.Name) != filepath.Join(dir, n) {
			continue
		}
		bits, err := hex.DecodeString(c.Bits)
		if err != nil {
			continue
		}
		if t.adopt(c.Name, c.Size, c.Piece, bits) {
			t.mu.Lock()
			t.etag, t.modified = c.ETag, c.Modified
			t.mu.Unlock()
			return
		}
	}
	if t.loadAria2(dir, ents) {
		return
	}
	// No progress record: a data file under the requested name is continued
	// after its current size when the server allows ranges
	if t.req.Out != "" && !strings.ContainsAny(t.req.Out, "/\\") {
		if st, err := os.Stat(filepath.Join(dir, t.req.Out)); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
			t.mu.Lock()
			t.contSize = st.Size()
			t.mu.Unlock()
		}
	}
}

// loadAria2 takes over the progress of an aria2 control file.
func (t *task) loadAria2(dir string, ents []os.DirEntry) bool {
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".aria2") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		piece, total, bits, err := parseAria2Control(b)
		if err != nil {
			continue
		}
		if t.adopt(strings.TrimSuffix(n, ".aria2"), total, piece, bits) {
			t.mu.Lock()
			t.adopted = filepath.Join(dir, n)
			t.mu.Unlock()
			t.e.logf(t.ref, "已沿用 aria2 的下載進度")
			return true
		}
	}
	return false
}

// adopt takes over saved progress when the data file is there and the
// numbers add up.
func (t *task) adopt(name string, size, piece int64, bits []byte) bool {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return false
	}
	st, err := os.Stat(filepath.Join(t.req.Dir, name))
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	n := int((size + piece - 1) / piece)
	if len(bits) != (n+7)/8 {
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
	p := t.adopted
	t.adopted = ""
	t.mu.Unlock()
	if p != "" {
		os.Remove(p)
	}
}

// parseAria2Control reads the piece length, total length and bitfield of
// an aria2 control file ("Control File (*.aria2) Format" in the aria2
// manual): version 1 is big-endian, version 0 host order (little-endian on
// QNAP's CPUs).
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
	p := 6 // version, extension
	u32 := func() (int64, bool) {
		if p+4 > len(b) {
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
	if p+16 > len(b) {
		return 0, 0, nil, bad
	}
	total = int64(bo.Uint64(b[p:]))
	p += 16 // total length, upload length
	bfLen, ok := u32()
	if !ok || total <= 0 || p+int(bfLen) > len(b) {
		return 0, 0, nil, bad
	}
	return piece, total, b[p : p+int(bfLen)], nil
}
