package lt

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/torrent"
)

// testCA is a certificate authority made for one test.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leaf is a server certificate for ips signed by ca.
func (ca *testCA) leaf(t *testing.T, ips ...net.IP) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "tracker"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IPAddresses: ips,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// httpsTracker is an HTTPS tracker on addr that counts its announces.
func httpsTracker(t *testing.T, cert tls.Certificate, addr string) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Write([]byte("d8:intervali1800e5:peers0:e"))
	}))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv.Listener.Close()
	srv.Listener = ln
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &n
}

func trackerPort(srv *httptest.Server) string {
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	return p
}

// A tracker whose certificate cannot be verified says so, even when the
// first listen socket (loopback) skipped the announce because it cannot
// reach the tracker's address.
func TestRealSourcesCertificate(t *testing.T) {
	own := ownIPv4()
	if own == nil {
		t.Skip("no address but loopback")
	}
	ip := net.IP(own)
	srv, _ := httpsTracker(t, newCA(t, "Unknown CA").leaf(t, ip), ":0")
	e, dir := utStart(t, "")
	tor, hash := utTorrent(t, "Cert", []utFile{{"a.bin", utRandom(utPiece)}}, "https://"+ip.String()+":"+trackerPort(srv)+"/announce")
	if _, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: filepath.Join(dir, "data")}); err != nil {
		t.Fatal(err)
	}
	var last string
	for end := time.Now().Add(30 * time.Second); last != TrackerCertificate; time.Sleep(200 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("tracker message %q", last)
		}
		if trs, err := e.Sources(hash); err == nil && len(trs) == 1 && trs[0].Fails > 0 {
			last = trs[0].Message
		}
	}
}

// Trackers are verified against the system's certificate authorities and
// the package's list together: one signed by an authority that is only in
// the list, or only in the system's file, is reached; one signed by neither
// is not.
func TestRealTrackerCA(t *testing.T) {
	scratch := t.TempDir()
	inList, inSystem, nowhere := newCA(t, "Listed CA"), newCA(t, "System CA"), newCA(t, "Unknown CA")
	list := filepath.Join(scratch, "cacert.pem")
	os.WriteFile(list, inList.pem, 0644)
	system := filepath.Join(scratch, "system.pem")
	os.WriteFile(system, inSystem.pem, 0644)
	t.Setenv("SSL_CERT_FILE", system) // the system's file, as OpenSSL finds it
	lo := net.ParseIP("127.0.0.1")
	srvL, nL := httpsTracker(t, inList.leaf(t, lo), "127.0.0.1:0")
	srvS, nS := httpsTracker(t, inSystem.leaf(t, lo), "127.0.0.1:0")
	srvN, nN := httpsTracker(t, nowhere.leaf(t, lo), "127.0.0.1:0")
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := newEngine(dcbtBin(t), dir)
	e.CAFile = list
	e.ApplyGlobal(engine.Global{PortFrom: 26891, PortTo: 26899})
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	var nHash string
	for i, srv := range []*httptest.Server{srvL, srvS, srvN} {
		tor, hash := utTorrent(t, "T"+itoa(i), []utFile{{"a.bin", utRandom(utPiece)}}, "https://127.0.0.1:"+trackerPort(srv)+"/announce")
		if _, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: filepath.Join(dir, "data")}); err != nil {
			t.Fatal(err)
		}
		nHash = hash
	}
	utWait(t, "announces to both trackers", 30*time.Second, func() bool { return nL.Load() > 0 && nS.Load() > 0 })
	utWait(t, "the unknown authority refused", 30*time.Second, func() bool {
		trs, _ := e.Sources(nHash)
		return len(trs) == 1 && trs[0].Fails > 0 && trs[0].Message == TrackerCertificate
	})
	if nN.Load() != 0 {
		t.Fatal("a tracker signed by an unknown authority was announced to")
	}
	if fi, err := os.Stat(filepath.Join(dir, "bt", "ca.pem")); err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("merged list: %v %v", fi.Mode(), err)
	}
}

// The hand-over fails when the name it made in the user's folder no longer
// is what it made: renamed meanwhile, with a link put in its place.
func TestRealHandOverRenamed(t *testing.T) {
	e, _ := utStart(t, "")
	var files []utFile
	for i := 0; i < 3000; i++ {
		files = append(files, utFile{fmt.Sprintf("d%02d/f%04d.bin", i%30, i), utRandom(64)})
	}
	for attempt := 0; attempt < 5; attempt++ {
		d := utScratch(t)
		name := "Many" + itoa(attempt)
		tor, hash := utTorrent(t, name, files, "")
		for _, f := range files {
			utPut(t, d.stage, name+"/"+f.path, f.data)
		}
		ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.stage, Check: true, UntrustedDir: true})
		if err != nil {
			t.Fatal(err)
		}
		utSeeding(t, e, ref)
		top := filepath.Join(d.dl, name)
		var moving, swapped atomic.Bool
		moving.Store(true)
		go func() {
			for moving.Load() {
				if _, err := os.Lstat(top); err == nil {
					if os.Rename(top, filepath.Join(d.dl, "aside")) == nil {
						utLink(t, d.victim, top)
						swapped.Store(true)
					}
					return
				}
			}
		}()
		err = e.MoveStorage(ref, d.dl, "")
		moving.Store(false)
		if err != nil {
			t.Fatal(err)
		}
		var st *engine.Status
		utWait(t, "move", 30*time.Second, func() bool {
			st, _ = e.Status(ref)
			return st != nil && !st.Moving && (filepath.Clean(st.Dir) == d.dl || st.MoveError != "")
		})
		if !swapped.Load() {
			t.Logf("attempt %d: the folder was not renamed in time", attempt)
			continue
		}
		if st.MoveError == "" {
			t.Fatalf("the hand-over reported success while %s is a link", top)
		}
		t.Logf("move error: %s", st.MoveError)
		utOnly(t, d.victim)
		return
	}
	t.Fatal("the folder was never renamed during a hand-over")
}

// adoptSetup: a torrent "Box" of a.bin (2 pieces) and b.bin (2 pieces), the
// user's folder ready for data.
func adoptSetup(t *testing.T) (*utDirs, []byte, []byte, []byte, string) {
	d := utScratch(t)
	a, b := utRandom(2*utPiece), utRandom(2*utPiece)
	tor, hash := utTorrent(t, "Box", []utFile{{"a.bin", a}, {"b.bin", b}}, utSeeder(t, append(append([]byte{}, a...), b...)))
	os.Mkdir(filepath.Join(d.dl, "Box"), 0755)
	os.Lchown(filepath.Join(d.dl, "Box"), utUID, utGID)
	return d, a, b, tor, hash
}

// putUser writes a file of the user in their folder.
func putUser(t *testing.T, d *utDirs, rel string, data []byte, uid int) {
	t.Helper()
	p := filepath.Join(d.dl, rel)
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
	os.Chown(p, uid, uid)
}

// addAdopt adds like dcd: again while dc-bt is still checking the data in
// the destination, which never holds up its other answers.
func addAdopt(t *testing.T, e *Engine, hash string, r engine.AddRequest) (string, error) {
	t.Helper()
	for end := time.Now().Add(60 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		ref, err := e.Add(hash, r)
		if err != engine.ErrPending {
			return ref, err
		}
		if _, err := e.StatusAll(); err != nil {
			t.Fatalf("dc-bt does not answer while it checks: %v", err)
		}
	}
	t.Fatal("still checking after a minute")
	return "", nil
}

func adoptReq(tor []byte, d *utDirs, required bool) engine.AddRequest {
	return engine.AddRequest{Torrent: tor, Dir: d.stage, UntrustedDir: true, OwnerUID: utUID, OwnerGID: utGID,
		AdoptFrom: d.dl, AdoptRoot: "Box", AdoptRequired: required}
}

// What is in the user's folder is only taken over as a plain file of the
// user, no larger than the torrent's, holding the torrent's data: a link, a
// second name of someone else's file, another account's file, a larger one
// and one that only shares the name are not, and then nothing is.
func TestRealAdoptRefused(t *testing.T) {
	e, _ := utStart(t, "")
	for _, c := range []struct {
		name string
		put  func(d *utDirs, a []byte)
	}{
		{"symlink", func(d *utDirs, a []byte) {
			utLink(t, filepath.Join(d.victim, "secret"), filepath.Join(d.dl, "Box", "a.bin"))
		}},
		{"hard link", func(d *utDirs, a []byte) {
			utHardLink(t, filepath.Join(d.victim, "secret"), filepath.Join(d.dl, "Box", "a.bin"))
		}},
		{"another account's", func(d *utDirs, a []byte) { putUser(t, d, "Box/a.bin", a, utUID+1) }},
		{"larger", func(d *utDirs, a []byte) { putUser(t, d, "Box/a.bin", append(append([]byte{}, a...), 'x'), utUID) }},
		{"only the name", func(d *utDirs, a []byte) { putUser(t, d, "Box/a.bin", utRandom(len(a)), utUID) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, a, b, tor, hash := adoptSetup(t)
			secret := append([]byte("SECRET "), utRandom(2*utPiece-7)...)
			utPut(t, d.victim, "secret", secret)
			c.put(d, a)
			putUser(t, d, "Box/b.bin", b, utUID)
			before, _ := os.ReadFile(filepath.Join(d.dl, "Box", "a.bin"))
			if _, err := addAdopt(t, e, hash, adoptReq(tor, d, true)); err != engine.ErrFolderNotPrivate {
				t.Fatalf("taken over: %v", err)
			}
			if e.Adopted(hash) {
				t.Fatal("reported taken over")
			}
			utOnly(t, d.stage) // nothing linked into the private folder
			utSame(t, filepath.Join(d.victim, "secret"), secret)
			if after, _ := os.ReadFile(filepath.Join(d.dl, "Box", "a.bin")); !bytes.Equal(after, before) {
				t.Fatal("the user's file changed")
			}
			// Not required: the torrent downloads as new, the user's files untouched
			ref, err := addAdopt(t, e, hash, adoptReq(tor, d, false))
			if err != nil || e.Adopted(ref) {
				t.Fatalf("add %v adopted %v", err, e.Adopted(ref))
			}
			utSeeding(t, e, ref)
			if after, _ := os.ReadFile(filepath.Join(d.dl, "Box", "a.bin")); !bytes.Equal(after, before) {
				t.Fatal("the user's file changed")
			}
			utSame(t, filepath.Join(d.victim, "secret"), secret)
			e.Remove(ref)
		})
	}
}

// A file of the user that holds the start of the torrent's (an interrupted
// download) is taken over and completed in place; a file that was missing is
// downloaded privately and handed over beside it.
func TestRealAdoptPartial(t *testing.T) {
	e, _ := utStart(t, "")
	d, a, b, tor, hash := adoptSetup(t)
	putUser(t, d, "Box/a.bin", a[:utPiece+100], utUID)
	ref, err := addAdopt(t, e, hash, adoptReq(tor, d, false))
	if err != nil || !e.Adopted(ref) {
		t.Fatalf("add %v adopted %v", err, e.Adopted(ref))
	}
	e.Resume(ref)
	st := utSeeding(t, e, ref)
	if filepath.Clean(st.Dir) != d.dl || st.Files[0].Path != "Box/a.bin" {
		t.Fatalf("status %+v", st)
	}
	utWait(t, "b.bin handed over", 10*time.Second, func() bool {
		got, _ := os.ReadFile(filepath.Join(d.dl, "Box", "b.bin"))
		return bytes.Equal(got, b)
	})
	for name, want := range map[string][]byte{"a.bin": a, "b.bin": b} {
		p := filepath.Join(d.dl, "Box", name)
		got, _ := os.ReadFile(p)
		var fs syscall.Stat_t
		syscall.Lstat(p, &fs)
		if !bytes.Equal(got, want) || fs.Uid != utUID || fs.Nlink != 2 {
			t.Errorf("%s: %d bytes right %v, uid %d, links %d", name, len(got), bytes.Equal(got, want), fs.Uid, fs.Nlink)
		}
	}
}

// seededBefore leaves a torrent "Show" (a.bin, b.bin) as a dc-bt before 1.5
// did after seeding it from the user's folder: options without "untrusted",
// the files given to the user. It returns dc-bt's folder.
func seededBefore(t *testing.T, d *utDirs) (dir string, a, b, tor []byte, hash string) {
	e, dir := utStart(t, "")
	a, b = utRandom(2*utPiece), utRandom(2*utPiece)
	tor, hash = utTorrent(t, "Show", []utFile{{"a.bin", a}, {"b.bin", b}}, utSeeder(t, append(append([]byte{}, a...), b...)))
	utPut(t, d.dl, "Show/a.bin", a)
	utPut(t, d.dl, "Show/b.bin", b)
	ref, err := e.Add(hash, engine.AddRequest{Torrent: tor, Dir: d.dl, Check: true})
	if err != nil {
		t.Fatal(err)
	}
	utSeeding(t, e, ref)
	e.SaveState()
	e.Stop()
	op := filepath.Join(dir, "bt", hash+".json")
	o, _ := os.ReadFile(op)
	os.WriteFile(op, bytes.Replace(o, []byte(`"untrusted":false,`), nil, 1), 0600)
	filepath.Walk(filepath.Join(d.dl, "Show"), func(p string, _ os.FileInfo, _ error) error { return os.Lchown(p, utUID, utGID) })
	return dir, a, b, tor, hash
}

// A torrent that seeded from the user's folder before (dc-bt 1.4) seeds
// again after the upgrade, from a private copy of its verified data, and a
// link the user puts in place of a file changes nothing the swarm gets.
func TestRealAdoptSeeding(t *testing.T) {
	d := utScratch(t)
	dir, a, _, tor, hash := seededBefore(t, d)
	e2, _ := utStart(t, dir)
	req := engine.AddRequest{Torrent: tor, Dir: d.stage, UntrustedDir: true, OwnerUID: utUID, OwnerGID: utGID,
		AdoptFrom: d.dl, AdoptRoot: "Show", AdoptRequired: true}
	ref, err := addAdopt(t, e2, hash, req)
	if err != nil || !e2.Adopted(ref) {
		t.Fatalf("add %v adopted %v", err, e2.Adopted(ref))
	}
	e2.Resume(ref)
	utSeeding(t, e2, ref)
	if got := peerBlock(t, hash, 0); !bytes.Equal(got, a[:utPiece]) {
		t.Fatalf("piece 0: %q", got[:16])
	}
	// The user replaces a.bin with a link to a secret: the swarm still gets
	// the torrent's data
	secret := append([]byte("SECRET "), utRandom(2*utPiece-7)...)
	utPut(t, d.victim, "secret", secret)
	utLink(t, filepath.Join(d.victim, "secret"), filepath.Join(d.dl, "Show", "a.bin"))
	if got := peerBlock(t, hash, 1); !bytes.Equal(got, a[utPiece:2*utPiece]) {
		t.Fatalf("piece 1 after the swap: %q", got[:16])
	}
	utSame(t, filepath.Join(d.victim, "secret"), secret)
}

// Taken over again after dc-bt lost the torrent (its saved state gone, the
// private folder still there): the user's file has a second name, which is
// the private copy itself, and that is fine.
func TestRealAdoptAgain(t *testing.T) {
	e, dir := utStart(t, "")
	d, a, b, tor, hash := adoptSetup(t)
	putUser(t, d, "Box/a.bin", a, utUID)
	putUser(t, d, "Box/b.bin", b, utUID)
	ref, err := addAdopt(t, e, hash, adoptReq(tor, d, true))
	if err != nil || !e.Adopted(ref) {
		t.Fatalf("add %v adopted %v", err, e.Adopted(ref))
	}
	utSeeding(t, e, ref)
	e.Stop()
	for _, ext := range []string{".resume", ".json"} {
		os.Remove(filepath.Join(dir, "bt", hash+ext))
	}
	e2, _ := utStart(t, dir)
	if _, err := addAdopt(t, e2, hash, adoptReq(tor, d, true)); err != nil || !e2.Adopted(hash) {
		t.Fatalf("again: %v adopted %v", err, e2.Adopted(hash))
	}
	st := utSeeding(t, e2, hash)
	if filepath.Clean(st.Dir) != d.dl {
		t.Fatalf("status %+v", st)
	}
}

// An administrator's task names no owner: files root wrote there are taken
// over too, in a folder that belongs to another account.
func TestRealAdoptRootFiles(t *testing.T) {
	e, _ := utStart(t, "")
	d, a, b, tor, hash := adoptSetup(t)
	putUser(t, d, "Box/a.bin", a, 0)
	putUser(t, d, "Box/b.bin", b, 0)
	req := adoptReq(tor, d, true)
	req.OwnerUID, req.OwnerGID = 0, 0
	ref, err := addAdopt(t, e, hash, req)
	if err != nil || !e.Adopted(ref) {
		t.Fatalf("add %v adopted %v", err, e.Adopted(ref))
	}
	utSeeding(t, e, ref)
}

// Some files chosen: a small chosen file whose only piece reaches into a
// file that is not chosen. When that file is not there the piece cannot be
// checked: the small file is left out (downloaded privately, the user's
// left alone) instead of nothing being taken over; when it is there it is
// read to check the piece, and the small file is taken over too.
func TestRealAdoptSelected(t *testing.T) {
	e, _ := utStart(t, "")
	for _, withSample := range []bool{false, true} {
		d := utScratch(t)
		v, m, s := utRandom(2*utPiece+100), utRandom(200), utRandom(utPiece)
		tor, hash := utTorrent(t, "Film", []utFile{{"v.bin", v}, {"m.nfo", m}, {"s.bin", s}},
			utSeeder(t, append(append(append([]byte{}, v...), m...), s...)))
		os.Mkdir(filepath.Join(d.dl, "Film"), 0755)
		os.Lchown(filepath.Join(d.dl, "Film"), utUID, utGID)
		putUser(t, d, "Film/v.bin", v, utUID)
		putUser(t, d, "Film/m.nfo", m, utUID)
		if withSample {
			putUser(t, d, "Film/s.bin", s, utUID)
		}
		req := engine.AddRequest{Torrent: tor, Dir: d.stage, UntrustedDir: true, OwnerUID: utUID, OwnerGID: utGID,
			Select: []int{0, 1}, AdoptFrom: d.dl, AdoptRoot: "Film", AdoptRequired: true}
		ref, err := addAdopt(t, e, hash, req)
		if err != nil || !e.Adopted(ref) {
			t.Fatalf("sample %v: add %v adopted %v", withSample, err, e.Adopted(ref))
		}
		utSeeding(t, e, ref)
		links := func(rel string) uint64 {
			var st syscall.Stat_t
			syscall.Lstat(filepath.Join(d.dl, rel), &st)
			return uint64(st.Nlink)
		}
		wantM := uint64(1)
		if withSample {
			wantM = 2
		}
		if links("Film/v.bin") != 2 || links("Film/m.nfo") != wantM {
			t.Fatalf("sample %v: links v %d m %d", withSample, links("Film/v.bin"), links("Film/m.nfo"))
		}
		for rel, want := range map[string][]byte{"Film/v.bin": v, "Film/m.nfo": m} {
			if got, _ := os.ReadFile(filepath.Join(d.dl, rel)); !bytes.Equal(got, want) {
				t.Fatalf("%s changed", rel)
			}
		}
		e.Remove(ref)
	}
}

// A copy of a torrent that seeded before, damaged by the user since, is not
// taken over: the piece that differs would be downloaded again into the
// user's file. Seeding ends instead and the file stays as it is.
func TestRealAdoptDamaged(t *testing.T) {
	d := utScratch(t)
	dir, _, b, tor, hash := seededBefore(t, d)
	damaged := append(append([]byte{}, b[:utPiece]...), utRandom(utPiece)...)
	putUser(t, d, "Show/b.bin", damaged, utUID)
	e, _ := utStart(t, dir)
	req := engine.AddRequest{Torrent: tor, Dir: d.stage, UntrustedDir: true, OwnerUID: utUID, OwnerGID: utGID,
		AdoptFrom: d.dl, AdoptRoot: "Show", AdoptRequired: true}
	if _, err := addAdopt(t, e, hash, req); err != engine.ErrFolderNotPrivate || e.Adopted(hash) {
		t.Fatalf("add %v adopted %v", err, e.Adopted(hash))
	}
	utOnly(t, d.stage)
	if got, _ := os.ReadFile(filepath.Join(d.dl, "Show", "b.bin")); !bytes.Equal(got, damaged) {
		t.Fatal("the user's file changed")
	}
}

// A file that has only a block in common with the torrent's is never taken
// over: a disk image (an administrator's, written by root) that starts with
// the same piece of zeros as the torrent's file and differs after it, and
// one whose only piece in common is that run of zeros.
func TestRealAdoptSharedBlock(t *testing.T) {
	e, _ := utStart(t, "")
	for _, c := range []struct {
		name string
		mine func(img []byte) []byte
	}{
		{"the rest differs", func(img []byte) []byte { return append(make([]byte, utPiece), utRandom(len(img)-utPiece)...) }},
		{"only the zeros", func(img []byte) []byte { return append(make([]byte, utPiece), utRandom(100)...) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := utScratch(t)
			img := append(make([]byte, utPiece), utRandom(3*utPiece)...)
			tor, hash := utTorrent(t, "Box", []utFile{{"db.img", img}}, utSeeder(t, img))
			mine := c.mine(img)
			utPut(t, d.dl, "Box/db.img", mine)
			req := adoptReq(tor, d, false)
			req.OwnerUID, req.OwnerGID = 0, 0
			ref, err := addAdopt(t, e, hash, req)
			if err != nil || e.Adopted(ref) {
				t.Fatalf("add %v adopted %v", err, e.Adopted(ref))
			}
			e.Resume(ref)
			utSeeding(t, e, ref) // downloaded privately
			utSame(t, filepath.Join(d.dl, "Box", "db.img"), mine)
			e.Remove(ref)
		})
	}
}

// A file the check found right that is deleted and made anew before the
// torrent is added is not taken over, even with the inode number of the old
// one (which the file system hands out again at once).
func TestRealAdoptReplaced(t *testing.T) {
	e, _ := utStart(t, "")
	d, a, b, tor, hash := adoptSetup(t)
	putUser(t, d, "Box/a.bin", a, utUID)
	putUser(t, d, "Box/b.bin", b, utUID)
	req := adoptReq(tor, d, false)
	if _, err := e.Add(hash, req); err != engine.ErrPending {
		t.Fatalf("first add: %v", err)
	}
	time.Sleep(2 * time.Second) // four pieces: checked long before
	p := filepath.Join(d.dl, "Box", "a.bin")
	var old syscall.Stat_t
	syscall.Lstat(p, &old)
	notes := append([]byte("my notes "), utRandom(len(a)-9)...)
	same := false
	for i := 0; i < 20 && !same; i++ {
		os.Remove(p)
		putUser(t, d, "Box/a.bin", notes, utUID)
		var now syscall.Stat_t
		syscall.Lstat(p, &now)
		same = now.Ino == old.Ino
	}
	if !same {
		t.Fatal("the new file never got the old inode number")
	}
	ref, err := addAdopt(t, e, hash, req)
	if err != nil || e.Adopted(ref) {
		t.Fatalf("add %v adopted %v", err, e.Adopted(ref))
	}
	e.Resume(ref)
	utSeeding(t, e, ref)
	if got, _ := os.ReadFile(p); !bytes.Equal(got, notes) {
		t.Fatal("the new file was written to")
	}
}

// Thousands of small files sharing a piece: each piece is read once, and one
// that reaches into a file that is not there is not read at all.
func TestRealAdoptManyFiles(t *testing.T) {
	e, _ := utStart(t, "")
	const n = 8000
	for _, all := range []bool{false, true} {
		d := utScratch(t)
		var files []utFile
		for i := 0; i < n; i++ {
			files = append(files, utFile{fmt.Sprintf("f%05d.txt", i), utRandom(16)})
		}
		files = append(files, utFile{"z.bin", utRandom(1000)})
		tor, hash := utTorrentPL(t, "Many", files, "", 1<<20)
		os.Mkdir(filepath.Join(d.dl, "Many"), 0755)
		os.Lchown(filepath.Join(d.dl, "Many"), utUID, utGID)
		for _, f := range files[:n] {
			putUser(t, d, "Many/"+f.path, f.data, utUID)
		}
		if all {
			putUser(t, d, "Many/z.bin", files[n].data, utUID)
		}
		req := engine.AddRequest{Torrent: tor, Dir: d.stage, UntrustedDir: true, OwnerUID: utUID, OwnerGID: utGID,
			AdoptFrom: d.dl, AdoptRoot: "Many"}
		start := time.Now()
		ref, err := addAdopt(t, e, hash, req)
		took := time.Since(start)
		if err != nil || e.Adopted(ref) != all || took > 10*time.Second {
			t.Fatalf("all there %v: add %v adopted %v in %v", all, err, e.Adopted(ref), took)
		}
		t.Logf("all there %v: %v", all, took)
		e.Remove(ref)
	}
}

// zeroTorrent is a torrent of one file of size bytes of zeros, in pieces of
// 16 MiB (size a multiple of it).
func zeroTorrent(t *testing.T, name string, size int64) ([]byte, string) {
	t.Helper()
	const plen = 16 << 20
	h := sha1.Sum(make([]byte, plen))
	pieces := bytes.Repeat(h[:], int(size/plen))
	tor := "d4:infod6:lengthi" + strconv.FormatInt(size, 10) + "e4:name" + itoa(len(name)) + ":" + name +
		"12:piece lengthi" + itoa(plen) + "e6:pieces" + itoa(len(pieces)) + ":" + string(pieces) + "ee"
	meta, err := torrent.Parse([]byte(tor))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(tor), meta.InfoHash
}

// adoptThreads counts dc-bt's threads that check data in a destination.
func adoptThreads(e *Engine) int {
	pid := e.cmd.Process.Pid
	ents, _ := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	n := 0
	for _, t := range ents {
		b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/comm", pid, t.Name()))
		if strings.TrimSpace(string(b)) == "dc-bt adopt" {
			n++
		}
	}
	return n
}

// At most two checks run at a time, one per account, and removing a torrent
// stops its check (large files of zeros keep them busy; they never write).
func TestRealAdoptSlots(t *testing.T) {
	e, _ := utStart(t, "")
	d := utScratch(t)
	const size = 64 << 30
	owners := []int{utUID, utUID, utUID + 1, utUID + 2}
	var tors [][]byte
	var hashes []string
	for i, owner := range owners {
		name := fmt.Sprintf("big%d.img", i)
		tor, hash := zeroTorrent(t, name, size)
		tors, hashes = append(tors, tor), append(hashes, hash)
		p := filepath.Join(d.dl, name)
		if err := os.WriteFile(p, nil, 0644); err != nil || os.Truncate(p, size) != nil {
			t.Fatal(err)
		}
		os.Chown(p, owner, owner)
	}
	add := func(i int) {
		t.Helper()
		req := engine.AddRequest{Torrent: tors[i], Dir: d.stage, UntrustedDir: true, OwnerUID: owners[i], OwnerGID: owners[i],
			AdoptFrom: d.dl, AdoptRoot: fmt.Sprintf("big%d.img", i)}
		if _, err := e.Add(hashes[i], req); err != engine.ErrPending {
			t.Fatalf("big%d: %v", i, err)
		}
	}
	running := func(want int, what string) {
		t.Helper()
		utWait(t, what, 5*time.Second, func() bool { return adoptThreads(e) == want })
		time.Sleep(500 * time.Millisecond)
		if n := adoptThreads(e); n != want {
			t.Fatalf("%s: %d checks", what, n)
		}
	}
	defer func() {
		for _, h := range hashes {
			e.Remove(h)
		}
	}()
	add(0)
	running(1, "the first check")
	add(1)
	running(1, "the account's second torrent waits")
	add(2)
	running(2, "another account's")
	add(3)
	running(2, "a third account waits")
	e.Remove(hashes[0])
	running(1, "the removed torrent's check stops")
	add(3)
	running(2, "the third account's starts")
	add(1)
	running(2, "the first account's second still waits")
	for _, h := range hashes {
		e.Remove(h)
	}
	running(0, "all stopped")
}
