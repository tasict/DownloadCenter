package release

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// GitHub is a small client for the Releases API of one repository.
type GitHub struct {
	Repo  string // owner/name
	Token string
	API   string // https://api.github.com unless set
	HTTP  *http.Client
}

// Release and Asset carry the fields of the GitHub API that are used here.
type Release struct {
	ID          int64   `json:"id"`
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	Body        string  `json:"body"`
	Draft       bool    `json:"draft"`
	Prerelease  bool    `json:"prerelease"`
	HTMLURL     string  `json:"html_url"`
	UploadURL   string  `json:"upload_url"`
	CreatedAt   string  `json:"created_at"`
	PublishedAt string  `json:"published_at"`
	Assets      []Asset `json:"assets"`
}

type Asset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	State              string `json:"state"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Asset finds an asset by name.
func (r *Release) Asset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// TokenFrom reads a token file (surrounding white space dropped).
func TokenFrom(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", errors.New(path + " is empty")
	}
	return t, nil
}

func (g *GitHub) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (g *GitHub) api(path string) string {
	base := g.API
	if base == "" {
		base = "https://api.github.com"
	}
	return base + "/repos/" + g.Repo + path
}

// do sends one request; out (when not nil) receives the JSON answer.
func (g *GitHub) do(method, u string, body io.Reader, ctype string, size int64, out any) error {
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "dcrelease")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if size > 0 {
		req.ContentLength = size
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(b))
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &e) == nil && e.Message != "" {
			msg = e.Message
		}
		return fmt.Errorf("%s %s: HTTP %d: %s", method, u, resp.StatusCode, msg)
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// Releases lists every release, drafts included when the token may see them.
func (g *GitHub) Releases() ([]Release, error) {
	var all []Release
	for page := 1; page <= 20; page++ {
		var rs []Release
		if err := g.do("GET", g.api(fmt.Sprintf("/releases?per_page=100&page=%d", page)), nil, "", 0, &rs); err != nil {
			return nil, err
		}
		all = append(all, rs...)
		if len(rs) < 100 {
			break
		}
	}
	return all, nil
}

// ReleaseByTag finds the release (draft or not) for a tag; nil when none.
func (g *GitHub) ReleaseByTag(tag string) (*Release, error) {
	rs, err := g.Releases()
	if err != nil {
		return nil, err
	}
	for i := range rs {
		if rs[i].TagName == tag {
			return &rs[i], nil
		}
	}
	return nil, nil
}

// CreateDraft creates a draft release for a tag at a commit.
func (g *GitHub) CreateDraft(tag, commit, name, body string, pre bool) (*Release, error) {
	in := map[string]any{"tag_name": tag, "target_commitish": commit, "name": name, "body": body, "draft": true, "prerelease": pre}
	b, _ := json.Marshal(in)
	var r Release
	if err := g.do("POST", g.api("/releases"), bytes.NewReader(b), "application/json", 0, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// DeleteAsset removes one asset of a release.
func (g *GitHub) DeleteAsset(a *Asset) error {
	return g.do("DELETE", g.api(fmt.Sprintf("/releases/assets/%d", a.ID)), nil, "", 0, nil)
}

// Upload adds a file to a release under its base name.
func (g *GitHub) Upload(r *Release, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	u := r.UploadURL
	if i := strings.Index(u, "{"); i >= 0 {
		u = u[:i]
	}
	if u == "" {
		u = "https://uploads.github.com/repos/" + g.Repo + fmt.Sprintf("/releases/%d/assets", r.ID)
	}
	return g.do("POST", u+"?name="+url.QueryEscape(name), f, "application/octet-stream", st.Size(), nil)
}

// Open streams the content of an asset (works for drafts and private
// repositories, unlike the browser download URL).
func (g *GitHub) Open(a *Asset) (io.ReadCloser, error) {
	req, err := http.NewRequest("GET", a.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "dcrelease")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: HTTP %d", a.Name, resp.StatusCode)
	}
	return resp.Body, nil
}

// Read downloads a small asset (SHA256SUMS, its signature) into memory.
func (g *GitHub) Read(a *Asset) ([]byte, error) {
	rc, err := g.Open(a)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 1<<20))
}

// Publish turns a draft into a published release with these notes.
func (g *GitHub) Publish(r *Release, notes string, pre bool) error {
	in := map[string]any{"draft": false, "body": notes, "prerelease": pre, "make_latest": fmt.Sprint(!pre)}
	b, _ := json.Marshal(in)
	return g.do("PATCH", g.api(fmt.Sprintf("/releases/%d", r.ID)), bytes.NewReader(b), "application/json", 0, nil)
}

// CheckRelease downloads the SHA256SUMS, its signature and every package of
// a release and verifies them with CheckSet. It returns the parsed list.
func (g *GitHub) CheckRelease(r *Release, version string, pub ed25519.PublicKey) (map[string]string, error) {
	sa, ga := r.Asset(SumsName), r.Asset(SigName)
	if sa == nil || ga == nil {
		return nil, fmt.Errorf("release %s has no %s or %s", r.TagName, SumsName, SigName)
	}
	sums, err := g.Read(sa)
	if err != nil {
		return nil, err
	}
	sig, err := g.Read(ga)
	if err != nil {
		return nil, err
	}
	hashes := map[string]string{}
	for _, arch := range Arches {
		n := AssetName(version, arch)
		a := r.Asset(n)
		if a == nil {
			return nil, fmt.Errorf("release %s has no %s", r.TagName, n)
		}
		rc, err := g.Open(a)
		if err != nil {
			return nil, err
		}
		h, size, err := HashReader(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		if size != a.Size {
			return nil, fmt.Errorf("%s: downloaded %d bytes, expected %d", n, size, a.Size)
		}
		hashes[n] = h
	}
	if a := r.Asset(InfoName); a != nil {
		b, err := g.Read(a)
		if err != nil {
			return nil, err
		}
		h, _, _ := HashReader(bytes.NewReader(b))
		hashes[InfoName] = h
	}
	return CheckSet(version, pub, sums, sig, hashes)
}

// --- the update feed (updates.json on GitHub Pages) ---

// Feed is the index of published releases that the product site and the
// in-app updater read. It is only an index: clients still check
// SHA256SUMS.sig with the public key before trusting a package.
type Feed struct {
	Schema           int           `json:"schema"`
	Generated        string        `json:"generated"`
	Repo             string        `json:"repo"`
	Latest           *string       `json:"latest"`
	LatestPrerelease *string       `json:"latest_prerelease"`
	Releases         []FeedRelease `json:"releases"`
}

type FeedRelease struct {
	Version    string               `json:"version"`
	Tag        string               `json:"tag"`
	Date       string               `json:"date"`
	Prerelease bool                 `json:"prerelease"`
	Schema     int                  `json:"schema"`
	URL        string               `json:"url"`
	Notes      string               `json:"notes"`
	SumsURL    string               `json:"sums_url"`
	SigURL     string               `json:"sig_url"`
	Assets     map[string]FeedAsset `json:"assets"`
}

type FeedAsset struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BuildFeed lists the published releases whose SHA256SUMS signature checks
// out, newest first. Releases that fail are left out and reported in skipped.
func (g *GitHub) BuildFeed(pub ed25519.PublicKey) (*Feed, []string, error) {
	rs, err := g.Releases()
	if err != nil {
		return nil, nil, err
	}
	f := &Feed{Schema: 1, Generated: time.Now().UTC().Format(time.RFC3339), Repo: g.Repo, Releases: []FeedRelease{}}
	var skipped []string
	for i := range rs {
		r := &rs[i]
		v := strings.TrimPrefix(r.TagName, "v")
		if r.Draft || !strings.HasPrefix(r.TagName, "v") || !ValidVersion(v) {
			continue
		}
		sa, ga := r.Asset(SumsName), r.Asset(SigName)
		if sa == nil || ga == nil {
			skipped = append(skipped, r.TagName+": no signed "+SumsName)
			continue
		}
		sums, err := g.Read(sa)
		if err == nil {
			var sig []byte
			if sig, err = g.Read(ga); err == nil {
				err = Verify(pub, sums, sig)
			}
		}
		var list map[string]string
		if err == nil {
			list, err = ParseSums(sums)
		}
		if err != nil {
			skipped = append(skipped, r.TagName+": "+err.Error())
			continue
		}
		schema := 1
		if a := r.Asset(InfoName); a != nil && list[InfoName] != "" {
			var info Info
			b, err := g.Read(a)
			if err == nil {
				if h, _, _ := HashReader(bytes.NewReader(b)); h != list[InfoName] {
					err = errors.New(InfoName + " does not match " + SumsName)
				} else {
					err = json.Unmarshal(b, &info)
				}
			}
			if err != nil {
				skipped = append(skipped, r.TagName+": "+err.Error())
				continue
			}
			if info.Schema > 0 {
				schema = info.Schema
			}
		}
		fr := FeedRelease{Version: v, Tag: r.TagName, Date: day(r.PublishedAt), Prerelease: r.Prerelease, Schema: schema, URL: r.HTMLURL, Notes: r.Body,
			SumsURL: sa.BrowserDownloadURL, SigURL: ga.BrowserDownloadURL, Assets: map[string]FeedAsset{}}
		for _, arch := range Arches {
			n := AssetName(v, arch)
			if a := r.Asset(n); a != nil && list[n] != "" {
				fr.Assets[arch] = FeedAsset{Name: n, URL: a.BrowserDownloadURL, Size: a.Size, SHA256: list[n]}
			}
		}
		f.Releases = append(f.Releases, fr)
	}
	sort.Slice(f.Releases, func(i, j int) bool { return Compare(f.Releases[i].Version, f.Releases[j].Version) > 0 })
	for i := range f.Releases {
		v := f.Releases[i].Version
		if f.Releases[i].Prerelease {
			if f.LatestPrerelease == nil {
				f.LatestPrerelease = &v
			}
		} else if f.Latest == nil {
			f.Latest = &v
		}
	}
	return f, skipped, nil
}

func day(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return ""
}
