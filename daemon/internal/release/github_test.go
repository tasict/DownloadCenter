package release

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub serves releases whose assets are kept in memory.
type fakeGitHub struct {
	srv      *httptest.Server
	releases []Release
	files    map[string][]byte // asset URL path -> content
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases") {
			json.NewEncoder(w).Encode(f.releases)
			return
		}
		if b, ok := f.files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// add publishes version v with the given files (name -> content).
func (f *fakeGitHub) add(v string, draft bool, files map[string][]byte) *Release {
	r := Release{ID: int64(len(f.releases) + 1), TagName: "v" + v, Draft: draft, PublishedAt: "2026-11-20T00:00:00Z"}
	for n, b := range files {
		p := "/assets/" + v + "/" + n
		f.files[p] = b
		r.Assets = append(r.Assets, Asset{ID: int64(len(f.files)), Name: n, Size: int64(len(b)), URL: f.srv.URL + p, BrowserDownloadURL: f.srv.URL + p})
	}
	f.releases = append(f.releases, r)
	return &f.releases[len(f.releases)-1]
}

// signedFiles is a complete signed release of v.
func signedFiles(t *testing.T, v string) (map[string][]byte, []byte) {
	pubFile, privFile, _ := GenerateKey()
	priv, _ := ParsePrivate(privFile)
	files := map[string][]byte{InfoName: []byte(`{"version":"` + v + `","schema":2}`)}
	hashes := map[string]string{}
	for _, a := range Arches {
		files[AssetName(v, a)] = []byte("package " + a)
	}
	for n, b := range files {
		h, _, _ := HashReader(strings.NewReader(string(b)))
		hashes[n] = h
	}
	sums := FormatSums(hashes)
	files[SumsName] = sums
	files[SigName] = Sign(priv, sums)
	return files, pubFile
}

// A release with an asset that is not part of it, or without one of its
// packages, is neither verified nor listed in the feed.
func TestReleaseExactlyItsManifest(t *testing.T) {
	gh := newFakeGitHub(t)
	files, pubFile := signedFiles(t, "1.4.0")
	pub, _ := ParsePublic(pubFile)
	g := &GitHub{Repo: "o/r", API: gh.srv.URL}
	good := gh.add("1.4.0", true, files)
	if _, err := g.CheckRelease(good, "1.4.0", pub); err != nil {
		t.Fatalf("complete draft: %v", err)
	}
	extra := map[string][]byte{"DownloadCenter_1.4.0_x86_64.qpkg.md5": []byte("x")}
	for n, b := range files {
		extra[n] = b
	}
	bad := gh.add("1.4.0", true, extra)
	if _, err := g.CheckRelease(bad, "1.4.0", pub); err == nil {
		t.Error("a draft with a foreign asset verified")
	}

	gh.releases = nil
	gh.add("1.4.0", false, extra)
	short := map[string][]byte{}
	for n, b := range files {
		if n != AssetName("1.4.0", "arm-x31") {
			short[n] = b
		}
	}
	gh.add("1.3.9", false, short) // signed list of 1.4.0 under another tag: also not its own
	f, skipped, err := g.BuildFeed(pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Releases) != 0 || len(skipped) != 2 {
		t.Errorf("feed %+v skipped %v", f.Releases, skipped)
	}
	gh.releases = nil
	gh.add("1.4.0", false, short)
	if f, skipped, _ = g.BuildFeed(pub); len(f.Releases) != 0 || len(skipped) != 1 {
		t.Errorf("a release without one of its packages: %+v %v", f.Releases, skipped)
	}
	gh.releases = nil
	gh.add("1.4.0", false, files)
	if f, _, _ = g.BuildFeed(pub); len(f.Releases) != 1 || f.Latest == nil || *f.Latest != "1.4.0" {
		t.Errorf("complete release: %+v", f)
	}
}
