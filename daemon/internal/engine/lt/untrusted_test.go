package lt

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/torrent"
)

// A regular user's torrent: dc-bt runs as root, the destination folder
// belongs to the user, who may put links (symbolic or hard) into it at any
// time. Nothing outside the user's folder may be written, moved into or
// read and sent to the swarm. The tests run as root, which can do anything,
// so they check targets, owners and contents themselves.

// The owner of the home folder: an unprivileged number without an account.
const utUID, utGID = 54321, 54321

const utPiece = 16384

type utFile struct {
	path string // below the torrent's name
	data []byte
}

func utRandom(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// utTorrent builds a torrent called name with real piece hashes and an
// optional tracker.
func utTorrent(t *testing.T, name string, files []utFile, announce string) ([]byte, string) {
	t.Helper()
	return utTorrentPL(t, name, files, announce, utPiece)
}

// utTorrentPL is utTorrent with pieces of plen bytes.
func utTorrentPL(t *testing.T, name string, files []utFile, announce string, plen int) ([]byte, string) {
	t.Helper()
	var all []byte
	var fl strings.Builder
	for _, f := range files {
		all = append(all, f.data...)
		fl.WriteString("d6:lengthi" + itoa(len(f.data)) + "e4:pathl")
		for _, c := range strings.Split(f.path, "/") {
			fl.WriteString(itoa(len(c)) + ":" + c)
		}
		fl.WriteString("ee")
	}
	var pieces []byte
	for i := 0; i < len(all); i += plen {
		h := sha1.Sum(all[i:min(i+plen, len(all))])
		pieces = append(pieces, h[:]...)
	}
	tor := "d"
	if announce != "" {
		tor += "8:announce" + itoa(len(announce)) + ":" + announce
	}
	tor += "4:infod5:filesl" + fl.String() + "e4:name" + itoa(len(name)) + ":" + name +
		"12:piece lengthi" + itoa(plen) + "e6:pieces" + itoa(len(pieces)) + ":" + string(pieces) + "ee"
	meta, err := torrent.Parse([]byte(tor))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(tor), meta.InfoHash
}

// utDirs: the core's staging folder (only root can enter it and its
// parent), the user's Download folder and a folder that must not change.
type utDirs struct {
	stage, dl, victim string
}

func utScratch(t *testing.T) *utDirs {
	t.Helper()
	base, err := os.MkdirTemp("", "ut")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	d := &utDirs{
		stage:  filepath.Join(base, "priv", "stage"),
		dl:     filepath.Join(base, "home", "Download"),
		victim: filepath.Join(base, "victim"),
	}
	for _, p := range []string{filepath.Join(base, "priv"), d.stage} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{filepath.Join(base, "home"), d.dl} {
		os.Mkdir(p, 0755)
		os.Lchown(p, utUID, utGID)
	}
	os.Mkdir(d.victim, 0755)
	return d
}

// utPut writes a file and its folders below dir, as root.
func utPut(t *testing.T, dir, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0755)
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// utLink makes a symbolic link the user owns.
func utLink(t *testing.T, target, at string) {
	t.Helper()
	os.Remove(at)
	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}
	os.Lchown(at, utUID, utGID)
}

// utHardLink replaces at with a hard link to target.
func utHardLink(t *testing.T, target, at string) {
	t.Helper()
	os.Remove(at)
	if err := os.Link(target, at); err != nil {
		t.Fatal(err)
	}
}

// utSame fails unless path is a regular file owned by root that holds
// want.
func utSame(t *testing.T, path string, want []byte) {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	got, _ := os.ReadFile(path)
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != 0 || !bytes.Equal(got, want) {
		t.Errorf("%s changed: mode %o uid %d, %d bytes (want %d)", path, st.Mode, st.Uid, len(got), len(want))
	}
}

// utOnly fails when dir holds anything but names.
func utOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Fatalf("%s holds %q, want %q", dir, got, names)
	}
}

// utStart runs a dc-bt of its own in dir (a new one when dir is "").
func utStart(t *testing.T, dir string) (*Engine, string) {
	t.Helper()
	bin := dcbtBin(t)
	if dir == "" {
		var err error
		if dir, err = os.MkdirTemp("", "bt"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
	}
	e := newEngine(bin, dir)
	e.ApplyGlobal(engine.Global{PortFrom: 26891, PortTo: 26899})
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Stop() })
	return e, dir
}

func utWait(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

// utSeeding waits until a torrent has all its wanted data.
func utSeeding(t *testing.T, e *Engine, ref string) *engine.Status {
	t.Helper()
	var st *engine.Status
	utWait(t, "seeding", 30*time.Second, func() bool {
		st, _ = e.Status(ref)
		return st != nil && st.Seeding && st.Total > 0 && st.Completed == st.Total
	})
	return st
}

// utMove asks for the data to be handed to dir and waits for the outcome.
func utMove(t *testing.T, e *Engine, ref, dir, root string) *engine.Status {
	t.Helper()
	if err := e.MoveStorage(ref, dir, root); err != nil {
		return &engine.Status{MoveError: err.Error()}
	}
	var st *engine.Status
	utWait(t, "move", 30*time.Second, func() bool {
		st, _ = e.Status(ref)
		return st != nil && !st.Moving && (filepath.Clean(st.Dir) == filepath.Clean(dir) || st.MoveError != "")
	})
	return st
}

// peerBlock asks dc-bt for the first block of a piece, as a peer of the
// swarm: what the swarm would receive.
func peerBlock(t *testing.T, infohash string, piece int) []byte {
	t.Helper()
	ih, _ := hexBytes(infohash)
	var last error
	// A dc-bt that has just started may not listen yet (the error kept is
	// the one from the port it listens on)
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(time.Second) {
		for port := 26891; port <= 26899; port++ {
			b, err := askBlock("127.0.0.1:"+strconv.Itoa(port), ih, piece)
			if err == nil {
				return b
			}
			if last == nil || !strings.Contains(err.Error(), "refused") {
				last = err
			}
		}
	}
	t.Fatalf("no block from dc-bt: %v", last)
	return nil
}

func askBlock(addr string, ih []byte, piece int) ([]byte, error) {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(40 * time.Second))
	hs := append([]byte("\x13BitTorrent protocol"), make([]byte, 8)...)
	id := make([]byte, 12)
	rand.Read(id)
	hs = append(append(append(hs, ih...), "-UT0001-"...), id...) // a new peer each time
	if _, err := c.Write(hs); err != nil {
		return nil, err
	}
	got := make([]byte, 68)
	if _, err := io.ReadFull(c, got); err != nil {
		return nil, err
	}
	if !bytes.Equal(got[28:48], ih) {
		return nil, errors.New("another torrent")
	}
	send := func(id byte, payload []byte) error {
		m := binary.BigEndian.AppendUint32(nil, uint32(1+len(payload)))
		_, err := c.Write(append(append(m, id), payload...))
		return err
	}
	if err := send(2, nil); err != nil { // interested
		return nil, err
	}
	asked := false
	for {
		var n uint32
		if err := binary.Read(c, binary.BigEndian, &n); err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		msg := make([]byte, n)
		if _, err := io.ReadFull(c, msg); err != nil {
			return nil, err
		}
		switch msg[0] {
		case 1: // unchoke
			if !asked {
				req := binary.BigEndian.AppendUint32(nil, uint32(piece))
				req = binary.BigEndian.AppendUint32(req, 0)
				req = binary.BigEndian.AppendUint32(req, utPiece)
				if err := send(6, req); err != nil {
					return nil, err
				}
				asked = true
			}
		case 7: // piece
			return msg[9:], nil
		}
	}
}

// utSeeder is a BitTorrent peer on loopback that has all of data and
// serves it to whoever asks; its tracker hands it out.
func utSeeder(t *testing.T, data []byte) (announce string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go utServe(c, data)
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	peer := string([]byte{127, 0, 0, 1, byte(port >> 8), byte(port)})
	tr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("d8:intervali1800e5:peers6:" + peer + "e"))
	}))
	t.Cleanup(tr.Close)
	return tr.URL + "/announce"
}

func utServe(c net.Conn, data []byte) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(60 * time.Second))
	hs := make([]byte, 68)
	if _, err := io.ReadFull(c, hs); err != nil {
		return
	}
	send := func(id byte, payload []byte) error {
		m := binary.BigEndian.AppendUint32(nil, uint32(1+len(payload)))
		_, err := c.Write(append(append(m, id), payload...))
		return err
	}
	out := append(append([]byte("\x13BitTorrent protocol"), make([]byte, 8)...), hs[28:48]...)
	c.Write(append(out, "-UT0002-abcdefghijkl"...))
	n := (len(data) + utPiece - 1) / utPiece
	bf := make([]byte, (n+7)/8)
	for i := 0; i < n; i++ {
		bf[i/8] |= 0x80 >> uint(i%8)
	}
	send(5, bf)
	for {
		var l uint32
		if binary.Read(c, binary.BigEndian, &l) != nil {
			return
		}
		if l == 0 {
			continue
		}
		msg := make([]byte, l)
		if _, err := io.ReadFull(c, msg); err != nil {
			return
		}
		switch msg[0] {
		case 2: // interested
			send(1, nil)
		case 6: // request
			idx, begin, size := binary.BigEndian.Uint32(msg[1:]), binary.BigEndian.Uint32(msg[5:]), binary.BigEndian.Uint32(msg[9:])
			off := int(idx)*utPiece + int(begin)
			if off+int(size) > len(data) {
				return
			}
			send(7, append(append([]byte{}, msg[1:9]...), data[off:off+int(size)]...))
		}
	}
}

// A link the user put where the data is about to go: neither the folder of
// the data nor the user's Download folder may lead out of it.
func TestRealUntrustedMoveLink(t *testing.T) {
	e, _ := utStart(t, "")
	for _, c := range []struct {
		name  string
		plant func(d *utDirs)
		dst   func(d *utDirs) string
	}{
		{"top folder", func(d *utDirs) { utLink(t, d.victim, filepath.Join(d.dl, "Movie")) }, func(d *utDirs) string { return d.dl }},
		{"download folder", func(d *utDirs) { os.Remove(d.dl); utLink(t, d.victim, d.dl) }, func(d *utDirs) string { return d.dl }},
		{"folder above", func(d *utDirs) {
			os.Remove(d.dl)
			utLink(t, filepath.Dir(d.victim), d.dl)
		}, func(d *utDirs) string { return filepath.Join(d.dl, "victim") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := utScratch(t)
			a, b := utRandom(2*utPiece), utRandom(utPiece+100)
			tor, hash := utTorrent(t, "Movie", []utFile{{"a.bin", a}, {"sub/b.bin", b}}, "")
			utPut(t, d.stage, "Movie/a.bin", a)
			utPut(t, d.stage, "Movie/sub/b.bin", b)
			ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.stage, Check: true, UntrustedDir: true})
			if err != nil {
				t.Fatal(err)
			}
			utSeeding(t, e, ref)
			c.plant(d)
			st := utMove(t, e, ref, c.dst(d), "Movie")
			t.Logf("move: dir %s error %q", st.Dir, st.MoveError)
			utOnly(t, d.victim)
			if st.MoveError == "" {
				t.Fatal("the move went through a link")
			}
			// The data is still there for another try
			if s, _ := e.Status(ref); s == nil || !s.Seeding || s.Completed != s.Total {
				t.Fatalf("after the refused move %+v", s)
			}
		})
	}
}

// A file the user swaps for a link (symbolic or hard) after the hand-over:
// the swarm must still get the torrent's data, never what the link reaches,
// also after dc-bt started again and dcd added the torrent again.
func TestRealUntrustedSeedLink(t *testing.T) {
	e, dir := utStart(t, "")
	d := utScratch(t)
	a, b := utRandom(2*utPiece), utRandom(2*utPiece)
	tor, hash := utTorrent(t, "Show", []utFile{{"a.bin", a}, {"b.bin", b}}, "")
	utPut(t, d.stage, "Show/a.bin", a)
	utPut(t, d.stage, "Show/b.bin", b)
	req := engine.AddRequest{Torrent: tor, Dir: d.stage, Check: true, UntrustedDir: true}
	ref, err := e.Add(hash, req)
	if err != nil {
		t.Fatal(err)
	}
	utSeeding(t, e, ref)
	if st := utMove(t, e, ref, d.dl, ""); st.MoveError != "" {
		t.Fatalf("hand-over: %s", st.MoveError)
	}
	// What the user got: their own files with the data, hard links of the
	// ones dc-bt seeds from in a folder of its own beside the temporary one
	for name, want := range map[string][]byte{"a.bin": a, "b.bin": b} {
		p := filepath.Join(d.dl, "Show", name)
		got, _ := os.ReadFile(p)
		var st syscall.Stat_t
		syscall.Lstat(p, &st)
		if !bytes.Equal(got, want) || st.Uid != utUID || st.Gid != utGID || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 2 {
			t.Errorf("%s: %d bytes, uid %d, mode %o, %d links", p, len(got), st.Uid, st.Mode, st.Nlink)
		}
	}
	var top, seed syscall.Stat_t
	syscall.Lstat(filepath.Join(d.dl, "Show"), &top)
	syscall.Lstat(d.stage+".seed", &seed)
	if top.Uid != utUID || top.Mode&0777 != 0775 || seed.Uid != 0 || seed.Mode&0777 != 0700 {
		t.Errorf("folders: user's %o uid %d, seed %o uid %d", top.Mode, top.Uid, seed.Mode, seed.Uid)
	}
	utOnly(t, d.stage)
	utOnly(t, d.stage+".seed", "Show")
	secretA := append([]byte("SECRET-A "), utRandom(2*utPiece-9)...)
	secretB := append([]byte("SECRET-B "), utRandom(2*utPiece-9)...)
	utPut(t, d.victim, "a", secretA)
	utPut(t, d.victim, "b", secretB)
	utLink(t, filepath.Join(d.victim, "a"), filepath.Join(d.dl, "Show", "a.bin"))
	utHardLink(t, filepath.Join(d.victim, "b"), filepath.Join(d.dl, "Show", "b.bin"))
	check := func(when string) {
		t.Helper()
		if got := peerBlock(t, hash, 0); !bytes.Equal(got, a[:utPiece]) {
			t.Errorf("%s: the swarm got %q... instead of the torrent's data", when, got[:min(16, len(got))])
		}
		if got := peerBlock(t, hash, 2); !bytes.Equal(got, b[:utPiece]) {
			t.Errorf("%s: the swarm got %q... instead of the torrent's data", when, got[:min(16, len(got))])
		}
	}
	check("running")
	// dc-bt starts again (as after a restart of the NAS), dcd adds the
	// torrent again where it believes the data is
	e.Stop()
	e2, _ := utStart(t, dir)
	req.Dir, req.Check = d.dl, false
	if _, err := e2.Add(hash, req); err != nil {
		t.Logf("added again: %v", err)
	}
	utSeeding(t, e2, ref)
	check("after a restart")
	utSame(t, filepath.Join(d.victim, "a"), secretA)
	utSame(t, filepath.Join(d.victim, "b"), secretB)
}

// Files chosen after the hand-over, and a check of the data (Retry): what
// libtorrent writes must not go through a link the user put in place.
func TestRealUntrustedWriteLink(t *testing.T) {
	e, _ := utStart(t, "")
	d := utScratch(t)
	a, b := utRandom(2*utPiece), utRandom(2*utPiece)
	tor, hash := utTorrent(t, "Clip", []utFile{{"a.bin", a}, {"b.bin", b}}, utSeeder(t, append(append([]byte{}, a...), b...)))
	utPut(t, d.stage, "Clip/a.bin", a)
	ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.stage, Check: true, Select: []int{0}, UntrustedDir: true})
	if err != nil {
		t.Fatal(err)
	}
	utSeeding(t, e, ref)
	if st := utMove(t, e, ref, d.dl, ""); st.MoveError != "" {
		t.Fatalf("hand-over: %s", st.MoveError)
	}
	target := append([]byte("ORIGINAL "), utRandom(100)...)
	utPut(t, d.victim, "target.conf", target)
	precious := append([]byte("PRECIOUS "), utRandom(2*utPiece-9)...)
	utPut(t, d.victim, "precious", precious)

	// b.bin is chosen now; the user has put a link where it will go
	utLink(t, filepath.Join(d.victim, "target.conf"), filepath.Join(d.dl, "Clip", "b.bin"))
	if err := e.SetFiles(ref, []int{0, 1}, nil); err != nil {
		t.Fatal(err)
	}
	e.Resume(ref)
	utWait(t, "b.bin downloaded", 30*time.Second, func() bool {
		st, _ := e.Status(ref)
		return st != nil && st.Total == int64(len(a)+len(b)) && st.Completed == st.Total
	})
	time.Sleep(time.Second)
	utSame(t, filepath.Join(d.victim, "target.conf"), target)

	// a.bin becomes a hard link to someone else's file, then the task is
	// retried: removed from the engine and added again with a check
	utHardLink(t, filepath.Join(d.victim, "precious"), filepath.Join(d.dl, "Clip", "a.bin"))
	e.Remove(ref)
	if _, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.dl, Check: true, UntrustedDir: true}); err != nil {
		t.Logf("added again: %v", err)
	}
	time.Sleep(5 * time.Second)
	utSame(t, filepath.Join(d.victim, "precious"), precious)
	utSame(t, filepath.Join(d.victim, "target.conf"), target)
	utOnly(t, d.victim, "precious", "target.conf")
}

// A file chosen after the hand-over reaches the user's folder too, and a
// torrent removed for good takes its own folder along.
func TestRealUntrustedLaterFile(t *testing.T) {
	e, _ := utStart(t, "")
	d := utScratch(t)
	a, b := utRandom(utPiece), utRandom(utPiece+7)
	tor, hash := utTorrent(t, "Pack", []utFile{{"a.bin", a}, {"x/b.bin", b}}, utSeeder(t, append(append([]byte{}, a...), b...)))
	utPut(t, d.stage, "Pack/a.bin", a)
	ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.stage, Check: true, Select: []int{0}, UntrustedDir: true})
	if err != nil {
		t.Fatal(err)
	}
	utSeeding(t, e, ref)
	if st := utMove(t, e, ref, d.dl, "Pack (1)"); st.MoveError != "" || st.Files[0].Path != "Pack (1)/a.bin" {
		t.Fatalf("hand-over: %q %+v", st.MoveError, st.Files)
	}
	e.SetFiles(ref, []int{0, 1}, nil)
	e.Resume(ref)
	p := filepath.Join(d.dl, "Pack (1)", "x", "b.bin")
	utWait(t, "b.bin handed over", 30*time.Second, func() bool {
		got, _ := os.ReadFile(p)
		return bytes.Equal(got, b)
	})
	var st, dir syscall.Stat_t
	syscall.Lstat(p, &st)
	syscall.Lstat(filepath.Dir(p), &dir)
	if st.Uid != utUID || st.Nlink != 2 || dir.Uid != utUID || dir.Mode&0777 != 0775 {
		t.Fatalf("b.bin uid %d links %d, its folder uid %d mode %o", st.Uid, st.Nlink, dir.Uid, dir.Mode)
	}
	e.Remove(ref)
	utWait(t, "seed folder removed", 10*time.Second, func() bool {
		_, err := os.Lstat(d.stage + ".seed")
		return os.IsNotExist(err)
	})
	if got, _ := os.ReadFile(p); !bytes.Equal(got, b) {
		t.Fatal("the user's copy went with the engine's")
	}
}

// Where the core still points at a folder the user can change: a new
// torrent is refused; one whose saved data is complete (a torrent of an
// earlier version seeding there) counts as having finished seeding, and
// dc-bt reads nothing there.
func TestRealUntrustedNotPrivate(t *testing.T) {
	e, dir := utStart(t, "")
	d := utScratch(t)
	a := utRandom(2 * utPiece)
	tor, hash := utTorrent(t, "Old", []utFile{{"a.bin", a}}, "")
	if _, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.dl, UntrustedDir: true}); err != engine.ErrFolderNotPrivate {
		t.Fatalf("new torrent in the user's folder: %v", err)
	}
	// As 1.4 had it: seeding in place from the user's folder
	utPut(t, d.dl, "Old/a.bin", a)
	ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.dl, Check: true})
	if err != nil {
		t.Fatal(err)
	}
	utSeeding(t, e, ref)
	e.SaveState()
	e.Stop()
	op := filepath.Join(dir, "bt", hash+".json")
	b, _ := os.ReadFile(op)
	os.WriteFile(op, bytes.Replace(b, []byte(`"untrusted":false,`), nil, 1), 0600)
	secret := append([]byte("SECRET "), utRandom(2*utPiece-7)...)
	utPut(t, d.victim, "s", secret)
	utLink(t, filepath.Join(d.victim, "s"), filepath.Join(d.dl, "Old", "a.bin"))
	e2, _ := utStart(t, dir)
	if _, err := e2.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.dl, UntrustedDir: true}); err != nil {
		t.Fatal(err)
	}
	st, err := e2.Status(ref)
	if err != nil || st.State != engine.Complete || st.Total != int64(len(a)) || st.Completed != st.Total {
		t.Fatalf("status %+v %v", st, err)
	}
	all, _ := e2.StatusAll()
	if all[ref] == nil || all[ref].State != engine.Complete {
		t.Fatalf("status_all %+v", all[ref])
	}
	ih, _ := hexBytes(hash)
	for port := 26891; port <= 26899; port++ {
		if b, err := askBlock("127.0.0.1:"+strconv.Itoa(port), ih, 0); err == nil {
			t.Fatalf("the swarm got %q", b[:16])
		}
	}
	e2.Remove(ref)
	if _, err := e2.Status(ref); err != engine.ErrNotFound {
		t.Fatalf("after Remove: %v", err)
	}
	utSame(t, filepath.Join(d.victim, "s"), secret)
}

// Options that cannot be read say nothing about whose torrent it is: the
// torrent waits until dcd adds it again instead of running unguarded.
func TestRealRestoreUnreadableOptions(t *testing.T) {
	e, dir := utStart(t, "")
	d := utScratch(t)
	a := utRandom(utPiece)
	tor, hash := utTorrent(t, "Opt", []utFile{{"a.bin", a}}, "")
	utPut(t, d.stage, "Opt/a.bin", a)
	ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.stage, Check: true, Guard: true})
	if err != nil {
		t.Fatal(err)
	}
	utSeeding(t, e, ref)
	e.SaveState()
	e.Stop()
	os.WriteFile(filepath.Join(dir, "bt", hash+".json"), []byte("{broken"), 0600)
	e2, _ := utStart(t, dir)
	if st, err := e2.Status(ref); err != engine.ErrNotFound {
		t.Fatalf("restored without knowing whose it is: %+v %v", st, err)
	}
	if _, err := e2.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.stage, Guard: true}); err != nil {
		t.Fatal(err)
	}
	utSeeding(t, e2, ref)
}

// A torrent of a single file is handed over as that file, under the name
// the core chose.
func TestRealUntrustedSingleFile(t *testing.T) {
	e, _ := utStart(t, "")
	d := utScratch(t)
	data := utRandom(utPiece)
	h := sha1.Sum(data)
	tor := []byte("d4:infod6:lengthi" + itoa(len(data)) + "e4:name5:s.bin12:piece lengthi" + itoa(utPiece) + "e6:pieces20:" + string(h[:]) + "ee")
	meta, err := torrent.Parse(tor)
	if err != nil {
		t.Fatal(err)
	}
	utPut(t, d.stage, "s.bin", data)
	utPut(t, d.dl, "s.bin", []byte("the user's own file"))
	ref, err := e.Add(meta.InfoHash, engine.AddRequest{Torrent: tor, Dir: d.stage, Check: true, UntrustedDir: true})
	if err != nil {
		t.Fatal(err)
	}
	utSeeding(t, e, ref)
	if st := utMove(t, e, ref, d.dl, "s.bin"); st.MoveError == "" {
		t.Fatal("a taken name was replaced")
	}
	st := utMove(t, e, ref, d.dl, "s (1).bin")
	if st.MoveError != "" || len(st.Files) != 1 || st.Files[0].Path != "s (1).bin" {
		t.Fatalf("hand-over %q %+v", st.MoveError, st.Files)
	}
	got, _ := os.ReadFile(filepath.Join(d.dl, "s (1).bin"))
	var fs syscall.Stat_t
	syscall.Lstat(filepath.Join(d.dl, "s (1).bin"), &fs)
	if !bytes.Equal(got, data) || fs.Uid != utUID || fs.Nlink != 2 {
		t.Fatalf("handed file: %d bytes uid %d links %d", len(got), fs.Uid, fs.Nlink)
	}
	if got, _ := os.ReadFile(filepath.Join(d.dl, "s.bin")); string(got) != "the user's own file" {
		t.Fatal("the user's file changed")
	}
}

// The core moved a regular user's unfinished data to another private
// folder (from a folder of the user's): dc-bt takes the new one and checks
// the data there.
func TestRealUntrustedNewStage(t *testing.T) {
	e, _ := utStart(t, "")
	d := utScratch(t)
	data := utRandom(2 * utPiece)
	tor, hash := utTorrent(t, "Mig", []utFile{{"a.bin", data}}, "")
	utPut(t, d.stage, "Mig/a.bin", data)
	ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.stage, Check: true, UntrustedDir: true})
	if err != nil {
		t.Fatal(err)
	}
	utSeeding(t, e, ref)
	stage2 := filepath.Join(filepath.Dir(d.stage), "stage2")
	os.Mkdir(stage2, 0700)
	utPut(t, stage2, "Mig/a.bin", data)
	if _, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: stage2, UntrustedDir: true}); err != nil {
		t.Fatal(err)
	}
	utWait(t, "the new folder", 20*time.Second, func() bool {
		st, _ := e.Status(ref)
		return st != nil && filepath.Clean(st.Dir) == stage2 && st.Seeding && st.Completed == st.Total
	})
}

// The destination may be named through a link that only root can have made
// (as /share/homes is): one owned by root in a folder of root's that others
// cannot write to, or a sticky one. Any other link on the way is refused.
// The files go to the account the core names, else to the folder's owner.
func TestRealUntrustedTrustedLink(t *testing.T) {
	e, _ := utStart(t, "")
	for i, c := range []struct {
		mode    os.FileMode
		linkUID int
		ok      bool
	}{{0755, 0, true}, {0755 | os.ModeSticky, 0, true}, {0777, 0, false}, {0755, utUID, false}} {
		d := utScratch(t)
		base := filepath.Dir(filepath.Dir(d.dl))
		links := filepath.Join(base, "links")
		os.Mkdir(links, 0755)
		os.Chmod(links, c.mode)
		os.Symlink("../home", filepath.Join(links, "homes"))
		os.Lchown(filepath.Join(links, "homes"), c.linkUID, c.linkUID)
		data := utRandom(utPiece)
		name := "L" + itoa(i)
		tor, hash := utTorrent(t, name, []utFile{{"a.bin", data}}, "")
		utPut(t, d.stage, name+"/a.bin", data)
		ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.stage, Check: true, UntrustedDir: true, OwnerUID: utUID + 1, OwnerGID: utGID + 1})
		if err != nil {
			t.Fatal(err)
		}
		utSeeding(t, e, ref)
		st := utMove(t, e, ref, filepath.Join(links, "homes", "Download"), "")
		var fs syscall.Stat_t
		syscall.Lstat(filepath.Join(d.dl, name, "a.bin"), &fs)
		switch {
		case c.ok && (st.MoveError != "" || fs.Uid != utUID+1 || fs.Gid != utGID+1 || fs.Nlink != 2):
			t.Errorf("%v: %q uid %d gid %d links %d", c, st.MoveError, fs.Uid, fs.Gid, fs.Nlink)
		case !c.ok && st.MoveError == "":
			t.Errorf("%v: handed over through a link others can make", c)
		}
	}
}
