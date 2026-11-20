package phpbox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits of a plugin package.
const (
	MaxPackage  = 2 << 20 // bytes of the uploaded file
	MaxUnpacked = 8 << 20 // bytes of the files in it
	MaxEntries  = 64      // files and directories in it
	sigSize     = 128     // the RSA-1024 signature after an add-on's tar
)

// Package formats.
const (
	FormatDLM   = "dlm"   // search module: tar.gz with INFO and the module
	FormatAddon = "addon" // add-on: tar, then a signature by the key inside
)

// PackageError is a package that cannot be installed. Msg is a fixed
// English sentence whose variable parts {0}, {1} are Args.
type PackageError struct {
	Msg  string
	Args []string
}

func (e *PackageError) Error() string {
	s := e.Msg
	for i, a := range e.Args {
		s = strings.Replace(s, "{"+strconv.Itoa(i)+"}", a, 1)
	}
	return s
}

func bad(msg string, args ...string) error { return &PackageError{Msg: msg, Args: args} }

// Messages of PackageError.
const (
	MsgTooLarge   = "The file is larger than 2 MiB"
	MsgNotPlugin  = "This is not a search module or add-on file"
	MsgOnlySearch = "Only search modules can be installed"
	MsgUnpacked   = "The package is too large when unpacked"
	MsgEntries    = "The package has too many files"
	MsgUnsafe     = "The package contains a link, a device or an unsafe file name"
	MsgMissing    = "The package lacks {0}"
	MsgField      = "The field {0} of the package is not valid"
	MsgDamaged    = "The add-on is damaged or was modified"
	MsgNoSearch   = "The add-on does not offer search"
)

// Package is an inspected plugin package: what it says about itself and
// its files, kept in memory. Nothing in it was run.
type Package struct {
	Format      string `json:"format"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"`
	Site        string `json:"site,omitempty"` // module: its site; add-on: website
	Author      string `json:"author,omitempty"`
	Entry       string `json:"entry"` // the PHP file to load
	Class       string `json:"class"`
	// Account: the plugin takes a user name and password (a module's
	// accountsupport, an add-on that declares IVerify).
	Account bool `json:"account"`
	// Add-ons only
	Hosts          []string          `json:"hosts,omitempty"`
	Domain         string            `json:"domain,omitempty"`
	Interfaces     []string          `json:"interfaces,omitempty"`
	Descriptions   map[string]string `json:"descriptions,omitempty"` // QTS language code -> text
	KeyFingerprint string            `json:"key_fingerprint,omitempty"`
	// SHA256 is the hash of the uploaded bytes (lower-case hex).
	SHA256 string `json:"sha256"`

	Files map[string][]byte `json:"-"` // path in the package -> content
}

var (
	identRe = regexp.MustCompile(`^\\?[A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff]*(\\[A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff]*)*$`)
	segRe   = regexp.MustCompile(`^[A-Za-z0-9._@+\- ()\[\]]{1,100}$`)
)

// Inspect reads a plugin package (the uploaded bytes and the file's name)
// without running anything: its size, the archive's entries (regular files
// and directories only, safe relative names, at most MaxEntries and
// MaxUnpacked bytes), the metadata and, for an add-on, the signature by
// the key it carries. Errors are *PackageError.
func Inspect(name string, data []byte) (*Package, error) {
	if len(data) > MaxPackage {
		return nil, bad(MsgTooLarge)
	}
	sum := sha256.Sum256(data)
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".host") {
		return nil, bad(MsgOnlySearch)
	}
	var p *Package
	var err error
	switch {
	case len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b:
		p, err = inspectDLM(data)
	case len(data) > 1024+sigSize:
		p, err = inspectAddon(data)
	default:
		err = bad(MsgNotPlugin)
	}
	if err != nil {
		return nil, err
	}
	p.SHA256 = hex.EncodeToString(sum[:])
	return p, nil
}

// cleanName checks a name in an archive and returns it without "./".
func cleanName(n string) (string, bool) {
	n = strings.TrimPrefix(n, "./")
	n = strings.TrimSuffix(n, "/")
	if n == "" || len(n) > 255 || strings.HasPrefix(n, "/") || !utf8.ValidString(n) {
		return "", false
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == "." || seg == ".." || !segRe.MatchString(seg) {
			return "", false
		}
	}
	return n, true
}

// readTar reads the entries of an archive with the package limits.
func readTar(r io.Reader) (map[string][]byte, error) {
	tr := tar.NewReader(r)
	files := map[string][]byte{}
	dirs := map[string]bool{}
	total := int64(0)
	for n := 0; ; n++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, bad(MsgNotPlugin)
		}
		if n >= MaxEntries {
			return nil, bad(MsgEntries)
		}
		name, ok := cleanName(h.Name)
		if !ok {
			return nil, bad(MsgUnsafe)
		}
		if files[name] != nil || dirs[name] {
			return nil, bad(MsgUnsafe)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			dirs[name] = true
		case tar.TypeReg:
			if h.Size < 0 || total+h.Size > MaxUnpacked {
				return nil, bad(MsgUnpacked)
			}
			b, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
			if err != nil || int64(len(b)) != h.Size {
				return nil, bad(MsgNotPlugin)
			}
			total += h.Size
			files[name] = b
		default:
			// Links, devices, FIFOs and anything else
			return nil, bad(MsgUnsafe)
		}
	}
	// A file may not sit where a directory is, nor below a file
	for name := range files {
		for d := path.Dir(name); d != "."; d = path.Dir(d) {
			if files[d] != nil {
				return nil, bad(MsgUnsafe)
			}
		}
	}
	return files, nil
}

// text reads an optional string field of a JSON object, at most max
// characters, without control characters.
func text(m map[string]json.RawMessage, key string, max int, required bool) (string, error) {
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		if required {
			return "", bad(MsgMissing, key)
		}
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		// Some packages write numbers where a string is expected
		var n json.Number
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&n) != nil {
			return "", bad(MsgField, key)
		}
		s = n.String()
	}
	s = strings.TrimSpace(s)
	if (required && s == "") || utf8.RuneCountInString(s) > max || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", bad(MsgField, key)
	}
	return s, nil
}

func flag(m map[string]json.RawMessage, key string) bool {
	raw, ok := m[key]
	if !ok {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "yes", "1":
			return true
		}
	}
	var n float64
	return json.Unmarshal(raw, &n) == nil && n != 0
}

func object(b []byte, what string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return nil, bad(MsgField, what)
	}
	return m, nil
}

func entryFile(files map[string][]byte, key, name string) (string, error) {
	n, ok := cleanName(name)
	if !ok {
		return "", bad(MsgField, key)
	}
	if files[n] == nil {
		return "", bad(MsgMissing, n)
	}
	return n, nil
}

func inspectDLM(data []byte) (*Package, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, bad(MsgNotPlugin)
	}
	// The tar headers and padding come on top of the files' bytes
	files, err := readTar(io.LimitReader(zr, MaxUnpacked+int64(MaxEntries+4)*1024))
	if err != nil {
		return nil, err
	}
	info, ok := files["INFO"]
	if !ok {
		return nil, bad(MsgMissing, "INFO")
	}
	m, err := object(info, "INFO")
	if err != nil {
		return nil, err
	}
	typ, err := text(m, "type", 20, true)
	if err != nil {
		return nil, err
	}
	if typ != "search" {
		return nil, bad(MsgOnlySearch)
	}
	p := &Package{Format: FormatDLM, Files: files, Account: flag(m, "accountsupport")}
	if p.Name, err = text(m, "name", 100, true); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		key string
		max int
		to  *string
	}{{"displayname", 100, &p.DisplayName}, {"description", 1000, &p.Description}, {"version", 40, &p.Version}, {"site", 500, &p.Site}} {
		if *f.to, err = text(m, f.key, f.max, false); err != nil {
			return nil, err
		}
	}
	module, err := text(m, "module", 255, true)
	if err != nil {
		return nil, err
	}
	if p.Entry, err = entryFile(files, "module", module); err != nil {
		return nil, err
	}
	if p.Class, err = text(m, "class", 200, true); err != nil {
		return nil, err
	}
	if !identRe.MatchString(p.Class) {
		return nil, bad(MsgField, "class")
	}
	return p, nil
}

// addonInterfaces are the interfaces an add-on may declare.
var addonInterfaces = map[string]bool{"ISearch": true, "IVerify": true, "IRss": true, "IDownload": true, "IPostProcess": true}

func inspectAddon(data []byte) (*Package, error) {
	body, sig := data[:len(data)-sigSize], data[len(data)-sigSize:]
	files, err := readTar(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	meta, ok := files["addon.json"]
	if !ok {
		return nil, bad(MsgMissing, "addon.json")
	}
	keyPEM, ok := files["addon.key"]
	if !ok {
		return nil, bad(MsgMissing, "addon.key")
	}
	pub, fp, err := addonKey(keyPEM)
	if err != nil {
		return nil, err
	}
	if !verifyAddon(pub, body, sig) {
		return nil, bad(MsgDamaged)
	}
	m, err := object(meta, "addon.json")
	if err != nil {
		return nil, err
	}
	p := &Package{Format: FormatAddon, Files: files, KeyFingerprint: fp}
	if p.Name, err = text(m, "name", 100, true); err != nil {
		return nil, err
	}
	if p.Author, err = text(m, "author", 100, false); err != nil {
		return nil, err
	}
	if p.Site, err = text(m, "website", 500, false); err != nil {
		return nil, err
	}
	if p.Domain, err = text(m, "domain", 255, false); err != nil {
		return nil, err
	}
	if p.Class, err = text(m, "class", 200, true); err != nil {
		return nil, err
	}
	if !identRe.MatchString(p.Class) {
		return nil, bad(MsgField, "class")
	}
	entry, err := text(m, "addon", 255, true)
	if err != nil {
		return nil, err
	}
	if p.Entry, err = entryFile(files, "addon", entry); err != nil {
		return nil, err
	}
	// version: an integer from 100, shown as x.yy
	v, err := text(m, "version", 10, true)
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 100 || n > 999999 {
		return nil, bad(MsgField, "version")
	}
	p.Version = strconv.Itoa(n/100) + "." + strconv.Itoa(n%100/10) + strconv.Itoa(n%10)
	if p.Hosts, err = stringList(m, "hosts"); err != nil {
		return nil, err
	}
	if p.Interfaces, err = stringList(m, "interface"); err != nil {
		return nil, err
	}
	search := false
	for _, i := range p.Interfaces {
		if !addonInterfaces[i] {
			return nil, bad(MsgField, "interface")
		}
		search = search || i == "ISearch"
		p.Account = p.Account || i == "IVerify"
	}
	if !search {
		return nil, bad(MsgNoSearch)
	}
	// description: per QTS language code, ENG required
	raw, ok := m["description"]
	if !ok {
		return nil, bad(MsgMissing, "description")
	}
	if err := json.Unmarshal(raw, &p.Descriptions); err != nil || p.Descriptions["ENG"] == "" {
		return nil, bad(MsgField, "description")
	}
	for k, v := range p.Descriptions {
		if len(k) > 8 || utf8.RuneCountInString(v) > 1000 {
			return nil, bad(MsgField, "description")
		}
	}
	p.Description = p.Descriptions["ENG"]
	return p, nil
}

// stringList reads a list of short strings, or one string of them
// separated by commas.
func stringList(m map[string]json.RawMessage, key string) ([]string, error) {
	raw, ok := m[key]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var l []string
	if err := json.Unmarshal(raw, &l); err != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, bad(MsgField, key)
		}
		l = strings.Split(s, ",")
	}
	out := []string{}
	for _, s := range l {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(s) > 255 || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) || len(out) == 50 {
			return nil, bad(MsgField, key)
		}
		out = append(out, s)
	}
	return out, nil
}

// addonKey reads the public key of an add-on: RSA, 1024 bits (the size of
// the signature), as a PKIX or PKCS #1 PEM block. The fingerprint is the
// SHA-256 of its PKIX encoding.
//
// crypto/rsa of the Go version in use accepts 1024-bit keys for
// verification (it refuses smaller ones); a test pins this, so that a Go
// that stops accepting them fails the tests instead of every add-on.
func addonKey(b []byte) (*rsa.PublicKey, string, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, "", bad(MsgField, "addon.key")
	}
	var pub *rsa.PublicKey
	switch block.Type {
	case "PUBLIC KEY":
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, "", bad(MsgField, "addon.key")
		}
		pub, _ = k.(*rsa.PublicKey)
	case "RSA PUBLIC KEY":
		k, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, "", bad(MsgField, "addon.key")
		}
		pub = k
	}
	if pub == nil || pub.N.BitLen() != 8*sigSize {
		return nil, "", bad(MsgField, "addon.key")
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, "", bad(MsgField, "addon.key")
	}
	sum := sha256.Sum256(der)
	return pub, hex.EncodeToString(sum[:]), nil
}

// verifyAddon checks the signature of an add-on: PKCS #1 v1.5 (block type
// 1, no DigestInfo, as a raw RSA signing tool makes it) over the lower-case
// hex SHA-1 of the tar, with the new line its packer adds or without.
func verifyAddon(pub *rsa.PublicKey, body, sig []byte) bool {
	sum := sha1.Sum(body)
	h := hex.EncodeToString(sum[:])
	for _, msg := range []string{h + "\n", h} {
		if rsa.VerifyPKCS1v15(pub, crypto.Hash(0), []byte(msg), sig) == nil {
			return true
		}
	}
	return false
}

// Extract writes the files of p into dir, which must not exist yet:
// directories 0700, files 0600 (dcd's own; the sandbox copies them for the
// plugin to read). Write to a new directory and rename it into place to
// replace a plugin.
func (p *Package) Extract(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	names := make([]string, 0, len(p.Files))
	for n := range p.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, ok := cleanName(n); !ok {
			return bad(MsgUnsafe)
		}
		target := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = f.Write(p.Files[n])
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Plugin is how to run p once its files are in dir.
func (p *Package) Plugin(dir string) Plugin {
	return Plugin{Format: p.Format, Dir: dir, Entry: p.Entry, Class: p.Class, Account: p.Account}
}

var errNoPHP = errors.New("no PHP")
