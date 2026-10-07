package core

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/torrent"
)

// AddOptions are the choices of the add dialog (and its API equivalents).
type AddOptions struct {
	Owner       string
	Admin       bool   // effective role of the owner
	TempDir     string // display or real path; "" = default (admins only)
	MoveDir     string // "" = do not move
	MoveSet     bool   // MoveDir was given explicitly (else the default applies)
	Select      []int  // torrents: selected file indices, nil = all
	AutoRemove  string // "", "completed", "seeded"; "default" = settings
	AccountMode string // auto | none | id | manual (URL tasks)
	AccountID   string
	ManualUser  string
	ManualPass  string
	Paused      bool
	Caller      string
	Headers     []string
	OutName     string
	Trackers    []string
	ContentOf   string // merge as another source of this task (same content)
	Proxy       string // URL tasks: "" or "auto", "none" or a proxy profile id
}

// AddResult describes the outcome of one add.
type AddResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Merged    bool   `json:"merged,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

// Errors returned by the add functions; the API layers map them to codes.
var (
	ErrDuplicate    = errors.New("duplicate")
	ErrBadURL       = errors.New("url_not_supported")
	ErrBadTorrent   = errors.New("torrent_invalid")
	ErrBadMagnet    = errors.New("magnet_invalid")
	ErrNotFound     = errors.New("task_not_found")
	ErrNotOwner     = errors.New("not_owner")
	ErrUnsupported  = errors.New("unsupported")
	ErrAccountLimit = errors.New("account_unavailable")
	ErrOtherOwner   = errors.New("duplicate_other_owner")
	ErrNoURL        = errors.New("url_unavailable")
	ErrNoBT         = errors.New("bt_unavailable")
	ErrMoving       = errors.New("task_moving")
	ErrBadPosition  = errors.New("invalid position")
	ErrPrivate      = errors.New("private_torrent")
)

// DupError carries the id of the existing task.
type DupError struct{ ID string }

func (e *DupError) Error() string { return "duplicate" }
func (e *DupError) Unwrap() error { return ErrDuplicate }

// URLHash is the stable id of a URL task: SHA-1 of the normalised URL.
func URLHash(raw string) string {
	s := sha1.Sum([]byte(NormalizeURL(raw)))
	return hex.EncodeToString(s[:])
}

// NormalizeURL lower-cases scheme and host, drops default ports and the
// fragment.
func NormalizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") || (u.Scheme == "ftp" && port == "21") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	u.Host = host
	u.Fragment = ""
	return u.String()
}

// UnwrapLink returns the link carried by a thunder:// (Xunlei), flashget://
// or qqdl:// (QQ) link: base64 of the real URL, with "AA…ZZ" or
// "[FLASHGET]…[FLASHGET]" around it for the first two. Anything else, and
// wrappers that do not decode, come back unchanged.
func UnwrapLink(raw string) string {
	s := strings.TrimSpace(raw)
	for i := 0; i < 3; i++ {
		scheme, rest, ok := strings.Cut(s, "://")
		if !ok {
			return s
		}
		var head, tail string
		switch strings.ToLower(scheme) {
		case "thunder":
			head, tail = "AA", "ZZ"
		case "flashget":
			// flashget://<base64>&<referrer id>
			head, tail = "[FLASHGET]", "[FLASHGET]"
			rest, _, _ = strings.Cut(rest, "&")
		case "qqdl":
		default:
			return s
		}
		b, ok := decodeBase64(strings.TrimRight(rest, "/"))
		if !ok {
			return s
		}
		d := string(b)
		if len(d) < len(head)+len(tail) || !strings.HasPrefix(d, head) || !strings.HasSuffix(d, tail) {
			return s
		}
		d = strings.TrimSpace(d[len(head) : len(d)-len(tail)])
		if d == "" {
			return s
		}
		s = escapeRawBytes(d)
	}
	return s
}

func decodeBase64(s string) ([]byte, bool) {
	if u, err := url.PathUnescape(s); err == nil {
		s = u
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}

// escapeRawBytes percent-encodes the bytes of a URL that is not UTF-8
// (old Chinese links carry GBK file names): the server gets the same bytes.
func escapeRawBytes(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 0x80 || c < 0x20 {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// urlSupported reports whether the URL engine can download this scheme now.
func (m *Manager) urlSupported(raw string) bool {
	s, ok := m.URL.(interface{ Supports(string) bool })
	if !ok {
		return m.URL != nil
	}
	u, err := url.Parse(raw)
	return err == nil && s.Supports(strings.ToLower(u.Scheme))
}

// KindOfURL returns http/ftp for a supported URL ("ftp" covers FTP, FTPS,
// SFTP and SCP for queues and limits), "" otherwise.
func KindOfURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return KindHTTP
	case "ftp", "ftps", "sftp", "scp":
		return KindFTP
	}
	return ""
}

// resolveFolders applies the folder rules for the owner.
func (m *Manager) resolveFolders(o *AddOptions) (temp, move string, err error) {
	s := m.Settings()
	if !o.Admin {
		home := m.UserDownloadDir(o.Owner)
		for _, d := range []string{o.TempDir, o.MoveDir} {
			if d == "" {
				continue
			}
			r, rerr := m.ResolvePath(o.Owner, d)
			if rerr != nil || !SameDir(r, home) {
				return "", "", ErrFolder
			}
		}
		return home, "", nil
	}
	temp = s.TempDir
	if o.TempDir != "" {
		if temp, err = m.ResolvePath(o.Owner, o.TempDir); err != nil {
			return "", "", err
		}
	}
	if st, serr := os.Stat(temp); serr != nil || !st.IsDir() {
		return "", "", ErrFolder
	}
	if err = usable(temp); err != nil {
		return "", "", err
	}
	if o.MoveSet || o.MoveDir != "" {
		if o.MoveDir != "" {
			if move, err = m.ResolvePath(o.Owner, o.MoveDir); err != nil {
				return "", "", err
			}
		}
	} else {
		move = s.MoveDir
	}
	if move != "" {
		if err = usable(move); err != nil {
			return "", "", err
		}
	}
	if move == temp {
		move = ""
	}
	return temp, move, nil
}

func (m *Manager) nextPosition() int {
	var p int
	m.db.QueryRow(`SELECT COALESCE(MAX(position), 0) FROM tasks WHERE removed_at = 0`).Scan(&p)
	for _, t := range m.live {
		if t.Position > p {
			p = t.Position
		}
	}
	return p + 1
}

func (m *Manager) newTask(hash, kind, source, name string, o *AddOptions, temp, move string) *Task {
	auto := o.AutoRemove
	if auto == "default" {
		auto = m.Settings().AutoRemove
	}
	if auto == "seeded" && kind != KindBT {
		auto = "completed"
	}
	caller := o.Caller
	if caller == "" {
		caller = "Download Center"
	}
	return &Task{
		Hash: hash, Owner: o.Owner, Kind: kind, Source: source, Name: name,
		State: StQueued, UserPaused: o.Paused, Position: m.nextPosition(),
		TempDir: temp, MoveDir: move, CreatedAt: time.Now().Unix(), AutoRemove: auto, Caller: caller,
	}
}

// resolveLink resolves a file-hosting link with the owner's accounts. A task
// added without an account resolves only what needs none (public Google
// Drive files), never with an account; other links stay plain (nil).
func (m *Manager) resolveLink(mode, owner, accountID, raw, proxy string) (*Resolved, error) {
	if mode != "none" {
		return m.Hosters.Resolve(owner, accountID, raw, proxy)
	}
	if pr, ok := m.Hosters.(interface {
		ResolvePublic(raw, proxy string) (*Resolved, error)
	}); ok {
		return pr.ResolvePublic(raw, proxy)
	}
	return nil, nil
}

// AddURL adds an HTTP/FTP download.
func (m *Manager) AddURL(raw string, o AddOptions) (*AddResult, error) {
	raw = UnwrapLink(raw)
	kind := KindOfURL(raw)
	if kind == "" {
		return nil, ErrBadURL
	}
	if !m.urlSupported(raw) {
		return nil, ErrNoURL
	}
	temp, move, err := m.resolveFolders(&o)
	if err != nil {
		return nil, err
	}
	hash := URLHash(raw)
	if t := m.Live(hash); t != nil {
		if t.Owner != o.Owner && !o.Admin {
			return nil, ErrOtherOwner
		}
		return &AddResult{ID: t.Hash, Name: t.Name, Duplicate: true}, &DupError{ID: t.Hash}
	}
	// File-hosting links are resolved before taking the manager lock: the
	// resolver talks to the service over the network
	if o.Proxy == "auto" {
		o.Proxy = ""
	}
	proxy, err := m.ProxyFor(o.Proxy, raw, o.Admin)
	if err != nil {
		return nil, err
	}
	var res *Resolved
	svc := ""
	if m.Hosters != nil {
		if s, ok := m.Hosters.Match(raw); ok {
			r, rerr := m.resolveLink(o.AccountMode, o.Owner, o.AccountID, raw, m.profileURL(proxy))
			if rerr != nil {
				return nil, rerr
			}
			// A result is used when it changes the address or adds headers
			// (a cookie account keeps the link and adds its cookies)
			if r != nil && r.URL != "" && (r.URL != raw || len(r.Headers) > 0) {
				res, svc = r, s
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ex, ok := m.live[hash]; ok {
		if ex.Owner != o.Owner && !o.Admin {
			return nil, ErrOtherOwner
		}
		return &AddResult{ID: ex.Hash, Name: ex.Name, Duplicate: true}, &DupError{ID: ex.Hash}
	}
	name := o.OutName
	if name == "" {
		name = nameFromURL(raw)
	}
	t := m.newTask(hash, kind, raw, name, &o, temp, move)
	t.Options.Headers = o.Headers
	t.Options.OutName = o.OutName
	t.Options.Proxy = o.Proxy
	t.Options.AccountMode, t.Options.AccountID = o.AccountMode, o.AccountID
	if t.Options.AccountMode == "" {
		t.Options.AccountMode = "auto"
	}
	if res != nil {
		t.Options.Hoster, t.Options.HosterAcct, t.Options.OrigURL = svc, res.Account, raw
		t.Options.Direct, t.Options.DirectHeaders, t.Options.ExpiresAt = res.URL, res.Headers, res.ExpiresAt
		t.Options.NoPages = res.NoPages
		if res.Name != "" {
			t.Name = torrent.SafeName(res.Name)
			if t.Options.OutName == "" {
				t.Options.OutName = t.Name
			}
		}
		if res.Size > 0 {
			t.Size = res.Size
		}
	}
	if o.AccountMode == "manual" && o.ManualUser != "" {
		m.db.SetSecret("task:"+hash, o.ManualUser+"\x00"+o.ManualPass)
	}
	t.WorkDir = m.workDirFor(temp, hash)
	// Re-adding a URL from the history replaces the old record
	m.db.X(`DELETE FROM tasks WHERE hash = ? AND removed_at > 0`, hash)
	m.db.X(`DELETE FROM task_files WHERE hash = ?`, hash)
	if err := m.saveTask(t); err != nil {
		return nil, err
	}
	m.live[hash] = t
	m.Log(hash, "Added")
	m.TaskEvent("task.added", t, map[string]any{"source": "url"})
	m.Kick()
	return &AddResult{ID: hash, Name: t.Name}, nil
}

func nameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	n := path.Base(u.Path)
	if n == "/" || n == "." {
		return u.Hostname()
	}
	if un, err := url.PathUnescape(n); err == nil {
		n = un
	}
	return torrent.SafeName(n)
}

// AddMagnet adds a magnet link. The metadata is fetched by the engine; when
// the dialog already probed it (ProbeMagnet), the saved .torrent is used.
func (m *Manager) AddMagnet(link string, o AddOptions) (*AddResult, error) {
	mg, err := torrent.ParseMagnet(link)
	if err != nil {
		return nil, ErrBadMagnet
	}
	if m.BTEngine() == nil {
		return nil, ErrNoBT
	}
	if b := m.probeTorrent(mg.InfoHash); b != nil {
		o.Trackers = append(o.Trackers, mg.Trackers...)
		return m.addTorrentBytes(b, link, o)
	}
	// A file-list probe still running for this magnet would hold the same
	// info hash in the engine: stop it, the task fetches the metadata itself
	m.CancelProbe(mg.InfoHash)
	temp, move, err := m.resolveFolders(&o)
	if err != nil {
		return nil, err
	}
	private := m.TorrentPrivate(mg.InfoHash)
	m.mu.Lock()
	defer m.mu.Unlock()
	if ex, ok := m.live[mg.InfoHash]; ok {
		if ex.Owner != o.Owner && !o.Admin {
			return nil, ErrOtherOwner
		}
		if private {
			return &AddResult{ID: ex.Hash, Name: ex.Name, Duplicate: true}, &DupError{ID: ex.Hash}
		}
		return m.mergeTrackers(ex, append(mg.Trackers, o.Trackers...), link)
	}
	t := m.newTask(mg.InfoHash, KindBT, strings.TrimSpace(link), mg.Name, &o, temp, move)
	if e := m.BTEngine(); e != nil {
		t.Engine = e.Name()
	}
	t.Options.Trackers = o.Trackers
	t.Options.Select = o.Select
	t.Options.Magnet = strings.TrimSpace(link)
	t.State = StMetadata
	m.stageTorrent(t, "")
	m.db.X(`DELETE FROM tasks WHERE hash = ? AND removed_at > 0`, t.Hash)
	if err := m.saveTask(t); err != nil {
		return nil, err
	}
	m.live[t.Hash] = t
	m.Log(t.Hash, "Added (magnet link)")
	m.TaskEvent("task.added", t, map[string]any{"source": "magnet"})
	m.Kick()
	return &AddResult{ID: t.Hash, Name: t.Name}, nil
}

// AddTorrent adds a .torrent file.
func (m *Manager) AddTorrent(b []byte, o AddOptions) (*AddResult, error) {
	return m.addTorrentBytes(b, "", o)
}

func (m *Manager) addTorrentBytes(b []byte, magnet string, o AddOptions) (*AddResult, error) {
	meta, err := torrent.Parse(b)
	if err != nil {
		return nil, ErrBadTorrent
	}
	if m.BTEngine() == nil {
		return nil, ErrNoBT
	}
	if o.ContentOf != "" {
		return m.addContentSource(o.ContentOf, b, meta, o)
	}
	temp, move, err := m.resolveFolders(&o)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ex, ok := m.live[meta.InfoHash]; ok {
		if ex.Owner != o.Owner && !o.Admin {
			return nil, ErrOtherOwner
		}
		// The same info hash is the same info dictionary: private for both
		if meta.Private {
			return &AddResult{ID: ex.Hash, Name: ex.Name, Duplicate: true}, &DupError{ID: ex.Hash}
		}
		return m.mergeTrackers(ex, append(meta.Trackers, o.Trackers...), "")
	}
	source := meta.Name + ".torrent"
	if magnet != "" {
		source = strings.TrimSpace(magnet)
	}
	t := m.newTask(meta.InfoHash, KindBT, source, meta.Name, &o, temp, move)
	if magnet != "" {
		t.Options.Magnet = source
		t.Source = source
	}
	if e := m.BTEngine(); e != nil {
		t.Engine = e.Name()
	}
	t.Comment = meta.Comment
	t.IsFolder = meta.IsFolder
	t.Options.Trackers = o.Trackers
	t.Options.Select = validSelection(o.Select, meta)
	t.Size = selectedSize(meta, t.Options.Select)
	if err := os.WriteFile(m.torrentPath(t.Hash), b, 0600); err != nil {
		return nil, err
	}
	m.stageTorrent(t, meta.Name)
	files := make([]FileRow, 0, len(meta.Files))
	for _, f := range meta.Files {
		prio := 1
		if t.Options.Select != nil && !containsInt(t.Options.Select, f.Index) {
			prio = 0
		}
		files = append(files, FileRow{Index: f.Index, Path: f.Path, Size: f.Size, Priority: prio})
	}
	t.FilesTotal = len(files)
	t.FilesChosen = len(files)
	if t.Options.Select != nil {
		t.FilesChosen = len(t.Options.Select)
	}
	m.db.X(`DELETE FROM tasks WHERE hash = ? AND removed_at > 0`, t.Hash)
	if err := m.saveTask(t); err != nil {
		return nil, err
	}
	m.db.Tx(func(tx *sqlTx) error { return m.storeFiles(tx, t.Hash, files) })
	m.live[t.Hash] = t
	m.Log(t.Hash, "Added (torrent)")
	kind := "torrent"
	if magnet != "" {
		kind = "magnet"
	}
	m.TaskEvent("task.added", t, map[string]any{"source": kind})
	m.Kick()
	return &AddResult{ID: t.Hash, Name: t.Name}, nil
}

// stageTorrent decides where a new torrent downloads. Like a URL task it
// goes to @DownloadCenterTemp/<hash> and moves to its destination once its
// data is complete. Data that already sits at the destination (a torrent
// added again) is checked and seeded where it is; data in the temporary
// location is checked and moved when seeding ends, as before.
func (m *Manager) stageTorrent(t *Task, name string) {
	if name != "" && name == filepath.Base(name) && name != "." && name != ".." {
		if p := filepath.Join(t.finalDir(), name); pathExists(p) {
			t.DataPath = p
			t.Options.Check = true
			return
		}
		if pathExists(filepath.Join(t.TempDir, name)) {
			t.Options.Check = true
			return
		}
	}
	if e := m.BTEngine(); e == nil || !e.Caps().MoveWhileSeeding {
		return
	}
	t.WorkDir = m.workDirFor(t.TempDir, t.Hash)
}

func validSelection(sel []int, meta *torrent.Meta) []int {
	if sel == nil {
		return nil
	}
	ok := map[int]bool{}
	for _, f := range meta.Files {
		if !f.Pad {
			ok[f.Index] = true
		}
	}
	var out []int
	for _, i := range sel {
		if ok[i] {
			out = append(out, i)
		}
	}
	if len(out) == 0 || len(out) == len(ok) {
		return nil
	}
	return out
}

func selectedSize(meta *torrent.Meta, sel []int) int64 {
	if sel == nil {
		return meta.Size
	}
	var n int64
	for _, f := range meta.Files {
		if containsInt(sel, f.Index) {
			n += f.Size
		}
	}
	return n
}

func containsInt(l []int, v int) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// TorrentPrivate reports whether a task's saved .torrent is a private
// torrent (BEP 27). A magnet without its metadata yet is not known to be.
func (m *Manager) TorrentPrivate(hash string) bool {
	b, err := os.ReadFile(m.torrentPath(hash))
	if err != nil {
		return false
	}
	meta, err := torrent.Parse(b)
	return err == nil && meta.Private
}

// mergeTrackers folds another link of the same torrent into ex; private
// torrents never get here. Caller holds m.mu.
func (m *Manager) mergeTrackers(ex *Task, trackers []string, link string) (*AddResult, error) {
	added := 0
	seen := map[string]bool{}
	for _, t := range ex.Options.Trackers {
		seen[t] = true
	}
	var add []string
	for _, t := range trackers {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			add = append(add, t)
		}
	}
	if len(add) > 0 {
		ex.Options.Trackers = append(ex.Options.Trackers, add...)
		added = len(add)
		if e := m.engineOf(ex); e != nil && ex.EngineRef != "" && ex.State != StDone {
			e.AddTrackers(ex.EngineRef, add)
		}
		m.markDirty(ex)
	}
	m.Log(ex.Hash, fmt.Sprintf("Merged another source of the same torrent (%d trackers added)", added))
	m.TaskEvent("task.merged", ex, map[string]any{"source": link, "trackers_added": added})
	return &AddResult{ID: ex.Hash, Name: ex.Name, Merged: true}, nil
}

// --- duplicate check (add dialog) ---

// CheckItem is one source to check before adding.
type CheckItem struct {
	Source  string `json:"source"`
	Torrent []byte `json:"-"`
}

// CheckResult tells the dialog what it would add.
type CheckResult struct {
	Source   string `json:"source"`
	Kind     string `json:"kind"`   // url | magnet | torrent
	Status   string `json:"status"` // new | in_list | downloaded | same_torrent | same_content
	TaskID   string `json:"task_id,omitempty"`
	TaskName string `json:"task_name,omitempty"`
	Name     string `json:"name,omitempty"`
	Private  bool   `json:"private,omitempty"` // the source or the matching task is a private torrent
	Hoster   string `json:"hoster,omitempty"`
}

// Check reports duplicates for the add dialog.
func (m *Manager) Check(owner string, admin bool, items []CheckItem, destDir string) []CheckResult {
	var out []CheckResult
	for _, it := range items {
		r := CheckResult{Source: it.Source}
		var hash, name string
		var meta *torrent.Meta
		it.Source = UnwrapLink(it.Source)
		switch {
		case len(it.Torrent) > 0:
			r.Kind = "torrent"
			if mt, err := torrent.Parse(it.Torrent); err == nil {
				meta, hash, name = mt, mt.InfoHash, mt.Name
			}
		case strings.HasPrefix(strings.ToLower(it.Source), "magnet:"):
			r.Kind = "magnet"
			if mg, err := torrent.ParseMagnet(it.Source); err == nil {
				hash, name = mg.InfoHash, mg.Name
			}
		default:
			r.Kind = "url"
			if KindOfURL(it.Source) != "" {
				hash, name = URLHash(it.Source), nameFromURL(it.Source)
				if m.Hosters != nil {
					if svc, ok := m.Hosters.Match(it.Source); ok {
						r.Hoster = svc
					}
				}
			}
		}
		r.Name = name
		r.Status = "new"
		if meta != nil && meta.Private {
			r.Private = true
		}
		if hash != "" {
			if t := m.Live(hash); t != nil && (admin || t.Owner == owner) {
				if m.TorrentPrivate(hash) {
					r.Private = true
				}
				r.TaskID, r.TaskName = t.Hash, t.Name
				if r.Kind == "url" {
					r.Status = "in_list"
				} else {
					r.Status = "same_torrent"
				}
			} else if t := m.Live(hash); t != nil {
				r.Status = "in_list"
			}
		}
		if r.Status == "new" && meta != nil {
			if id, private := m.sameContent(owner, admin, meta); id != "" {
				r.Status, r.TaskID = "same_content", id
				if private {
					r.Private = true
				}
			}
		}
		if r.Status == "new" && name != "" && destDir != "" {
			if st, err := os.Stat(filepath.Join(destDir, name)); err == nil {
				if meta == nil || st.IsDir() || st.Size() == meta.Size {
					r.Status = "downloaded"
				}
			}
		}
		out = append(out, r)
	}
	return out
}

// sameContent finds a live torrent task with the same file list (names and
// sizes) or the same BitTorrent v2 file roots, and whether its torrent is
// private.
func (m *Manager) sameContent(owner string, admin bool, meta *torrent.Meta) (string, bool) {
	for _, t := range m.List() {
		if t.Kind != KindBT || t.Hash == meta.InfoHash || (!admin && t.Owner != owner) {
			continue
		}
		b, err := os.ReadFile(m.torrentPath(t.Hash))
		if err != nil {
			continue
		}
		other, err := torrent.Parse(b)
		if err != nil || other.Name != meta.Name || len(other.Files) == 0 {
			continue
		}
		if sameFiles(other, meta) {
			return t.Hash, other.Private
		}
	}
	return "", false
}

func sameFiles(a, b *torrent.Meta) bool {
	fa, fb := realFiles(a), realFiles(b)
	if len(fa) != len(fb) {
		return false
	}
	for i := range fa {
		if fa[i].Path != fb[i].Path || fa[i].Size != fb[i].Size {
			return false
		}
		if fa[i].Root != "" && fb[i].Root != "" && fa[i].Root != fb[i].Root {
			return false
		}
	}
	return true
}

func realFiles(m *torrent.Meta) []torrent.File {
	var out []torrent.File
	for _, f := range m.Files {
		if !f.Pad {
			out = append(out, f)
		}
	}
	return out
}

// --- magnet probing (file list before adding) ---

type probe struct {
	started time.Time
	ref     string
	eng     engine.Engine
	meta    *torrent.Meta
	data    []byte
	err     string
}

var (
	probeMu sync.Mutex
	probes  = map[string]*probe{}
)

func (m *Manager) probeDir(hash string) string { return filepath.Join(m.DataDir, "probe", hash) }

func (m *Manager) probeTorrent(hash string) []byte {
	probeMu.Lock()
	defer probeMu.Unlock()
	if p := probes[hash]; p != nil && p.data != nil {
		return p.data
	}
	return nil
}

// ProbeMagnet starts (or polls) fetching a magnet's metadata. It returns the
// parsed torrent when ready, nil while pending.
func (m *Manager) ProbeMagnet(link string) (*torrent.Meta, string, error) {
	mg, err := torrent.ParseMagnet(link)
	if err != nil {
		return nil, "", ErrBadMagnet
	}
	if t := m.Live(mg.InfoHash); t != nil {
		if b, err := os.ReadFile(m.torrentPath(t.Hash)); err == nil {
			meta, _ := torrent.Parse(b)
			return meta, mg.InfoHash, nil
		}
	}
	probeMu.Lock()
	defer probeMu.Unlock()
	p := probes[mg.InfoHash]
	if p != nil && p.meta != nil {
		return p.meta, mg.InfoHash, nil
	}
	if p != nil && p.err != "" {
		delete(probes, mg.InfoHash)
		return nil, mg.InfoHash, errors.New(p.err)
	}
	if p == nil {
		e := m.BTEngine()
		if e == nil {
			return nil, mg.InfoHash, ErrNoBT
		}
		dir := m.probeDir(mg.InfoHash)
		os.MkdirAll(dir, 0700)
		ref, err := e.Add("", engine.AddRequest{Magnet: link, Dir: dir, MetadataOnly: true, SeedTime: -1})
		if err != nil {
			return nil, mg.InfoHash, err
		}
		p = &probe{started: time.Now(), ref: ref, eng: e}
		probes[mg.InfoHash] = p
		return nil, mg.InfoHash, nil
	}
	// Poll the engine
	if b, err := os.ReadFile(filepath.Join(m.probeDir(mg.InfoHash), mg.InfoHash+".torrent")); err == nil {
		if meta, perr := torrent.Parse(b); perr == nil {
			p.meta, p.data = meta, b
			p.eng.Remove(p.ref)
			os.RemoveAll(m.probeDir(mg.InfoHash))
			go func(h string) { time.Sleep(30 * time.Minute); probeMu.Lock(); delete(probes, h); probeMu.Unlock() }(mg.InfoHash)
			return meta, mg.InfoHash, nil
		}
	}
	if st, err := p.eng.Status(p.ref); err == nil && st.State == engine.Error {
		p.eng.Remove(p.ref)
		delete(probes, mg.InfoHash)
		return nil, mg.InfoHash, errors.New("Cannot get the file list of this magnet link")
	}
	if time.Since(p.started) > 10*time.Minute {
		p.eng.Remove(p.ref)
		delete(probes, mg.InfoHash)
		os.RemoveAll(m.probeDir(mg.InfoHash))
		return nil, mg.InfoHash, errors.New("Timed out getting the file list")
	}
	return nil, mg.InfoHash, nil
}

// CancelProbe stops a pending probe.
func (m *Manager) CancelProbe(hash string) {
	probeMu.Lock()
	defer probeMu.Unlock()
	if p := probes[hash]; p != nil {
		if p.meta == nil {
			p.eng.Remove(p.ref)
		}
		delete(probes, hash)
		os.RemoveAll(m.probeDir(hash))
	}
}
