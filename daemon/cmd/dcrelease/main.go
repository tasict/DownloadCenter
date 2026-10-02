// dcrelease makes, checks and publishes Download Center releases. It is run
// by tools/release.sh on the build machine and by the GitHub workflows.
//
//	dcrelease keygen  -key FILE -pub FILE         new ed25519 signing key pair (never overwrites)
//	dcrelease notes   -version V [-changelog F]   print the CHANGELOG.md section of a version
//	dcrelease sums    -version V -dir DIR         write DIR/SHA256SUMS for the packages of V
//	dcrelease sign    -key FILE -dir DIR          write DIR/SHA256SUMS.sig
//	dcrelease check   -version V -dir DIR -pub F  verify the packages, list and signature in DIR
//	dcrelease draft   -repo R -version V -commit SHA -dir DIR [-changelog F]
//	                                              create (or refill) the draft release and upload
//	dcrelease verify  -repo R -tag T -pub F       download the draft's files and verify them
//	dcrelease publish -repo R -tag T [-changelog F]
//	                                              publish the draft with its CHANGELOG notes
//	dcrelease feed    -repo R -pub F -out FILE    write updates.json from the published releases
//
// The GitHub token comes from -token-file, else from $GITHUB_TOKEN.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"downloadcenter/internal/release"
	"downloadcenter/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "keygen":
		err = keygen(args)
	case "notes":
		err = notes(args)
	case "sums":
		err = sums(args)
	case "sign":
		err = sign(args)
	case "check":
		err = check(args)
	case "draft":
		err = draft(args)
	case "verify":
		err = verify(args)
	case "publish":
		err = publish(args)
	case "feed":
		err = feed(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dcrelease "+cmd+": "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: dcrelease keygen|notes|sums|sign|check|draft|verify|publish|feed [flags] (see the source header)")
	os.Exit(2)
}

type opts struct {
	fs                                           *flag.FlagSet
	key, pub, dir, version, changelog, repo, tag *string
	commit, out, tokenFile                       *string
}

func parse(name string, args []string) *opts {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	o := &opts{fs: fs,
		key: fs.String("key", "", "private key file"), pub: fs.String("pub", "", "public key file"),
		dir: fs.String("dir", "", "directory with the packages"), version: fs.String("version", "", "version, e.g. 1.0.0"),
		changelog: fs.String("changelog", "CHANGELOG.md", "CHANGELOG.md"), repo: fs.String("repo", "", "GitHub repository owner/name"),
		tag: fs.String("tag", "", "release tag, e.g. v1.0.0"), commit: fs.String("commit", "", "commit to tag"),
		out: fs.String("out", "", "output file"), tokenFile: fs.String("token-file", "", "file with a GitHub token (else $GITHUB_TOKEN)")}
	fs.Parse(args)
	return o
}

func need(vals ...*string) error {
	for _, v := range vals {
		if *v == "" {
			return errors.New("missing a required flag")
		}
	}
	return nil
}

func (o *opts) versionFromTag() (string, error) {
	if *o.version != "" {
		return *o.version, nil
	}
	v := strings.TrimPrefix(*o.tag, "v")
	if !strings.HasPrefix(*o.tag, "v") || !release.ValidVersion(v) {
		return "", fmt.Errorf("tag %q is not v<version>", *o.tag)
	}
	return v, nil
}

func (o *opts) github() (*release.GitHub, error) {
	if *o.repo == "" {
		return nil, errors.New("missing -repo")
	}
	tok := os.Getenv("GITHUB_TOKEN")
	if *o.tokenFile != "" {
		t, err := release.TokenFrom(*o.tokenFile)
		if err != nil {
			return nil, err
		}
		tok = t
	}
	if tok == "" {
		return nil, errors.New("no GitHub token (-token-file or $GITHUB_TOKEN)")
	}
	return &release.GitHub{Repo: *o.repo, Token: tok}, nil
}

func (o *opts) publicKey() (pub []byte, err error) {
	if *o.pub == "" {
		return nil, errors.New("missing -pub")
	}
	return os.ReadFile(*o.pub)
}

func keygen(args []string) error {
	o := parse("keygen", args)
	if err := need(o.key, o.pub); err != nil {
		return err
	}
	for _, p := range []string{*o.key, *o.pub} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s already exists; a new key would make earlier releases unverifiable by older versions", p)
		}
	}
	pubFile, privFile, err := release.GenerateKey()
	if err != nil {
		return err
	}
	if err := os.WriteFile(*o.key, privFile, 0600); err != nil {
		return err
	}
	if err := os.WriteFile(*o.pub, pubFile, 0644); err != nil {
		return err
	}
	pub, _ := release.ParsePublic(pubFile)
	fmt.Printf("key %s: private key in %s (keep it secret), public key in %s (commit it)\n", release.KeyID(pub), *o.key, *o.pub)
	return nil
}

func notes(args []string) error {
	o := parse("notes", args)
	if err := need(o.version); err != nil {
		return err
	}
	b, err := os.ReadFile(*o.changelog)
	if err != nil {
		return err
	}
	n, err := release.Notes(b, *o.version)
	if err != nil {
		return err
	}
	fmt.Println(n)
	return nil
}

func sums(args []string) error {
	o := parse("sums", args)
	if err := need(o.version, o.dir); err != nil {
		return err
	}
	list := map[string]string{}
	for _, a := range release.Arches {
		n := release.AssetName(*o.version, a)
		h, _, err := release.HashFile(filepath.Join(*o.dir, n))
		if err != nil {
			return err
		}
		list[n] = h
	}
	// release.json tells clients the database layout of this build (signed with the list)
	info, _ := json.Marshal(release.Info{Version: *o.version, Schema: store.SchemaVersion})
	ip := filepath.Join(*o.dir, release.InfoName)
	if err := os.WriteFile(ip, append(info, '\n'), 0644); err != nil {
		return err
	}
	h, _, err := release.HashFile(ip)
	if err != nil {
		return err
	}
	list[release.InfoName] = h
	return os.WriteFile(filepath.Join(*o.dir, release.SumsName), release.FormatSums(list), 0644)
}

func sign(args []string) error {
	o := parse("sign", args)
	if err := need(o.key, o.dir); err != nil {
		return err
	}
	kb, err := os.ReadFile(*o.key)
	if err != nil {
		return err
	}
	priv, err := release.ParsePrivate(kb)
	if err != nil {
		return err
	}
	s, err := os.ReadFile(filepath.Join(*o.dir, release.SumsName))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*o.dir, release.SigName), release.Sign(priv, s), 0644)
}

func check(args []string) error {
	o := parse("check", args)
	if err := need(o.version, o.dir); err != nil {
		return err
	}
	pb, err := o.publicKey()
	if err != nil {
		return err
	}
	pub, err := release.ParsePublic(pb)
	if err != nil {
		return err
	}
	s, err := os.ReadFile(filepath.Join(*o.dir, release.SumsName))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(*o.dir, release.SigName))
	if err != nil {
		return err
	}
	hashes := map[string]string{}
	for _, n := range append(assetNames(*o.version), release.InfoName) {
		if h, _, err := release.HashFile(filepath.Join(*o.dir, n)); err == nil {
			hashes[n] = h
		}
	}
	if _, err := release.CheckSet(*o.version, pub, s, sig, hashes); err != nil {
		return err
	}
	fmt.Printf("%s: %d packages, list and signature (key %s) check out\n", *o.version, len(release.Arches), release.KeyID(pub))
	return nil
}

// draft creates the draft release, or refills a draft left by an earlier
// attempt; a published release is never touched.
func draft(args []string) error {
	o := parse("draft", args)
	if err := need(o.version, o.commit, o.dir); err != nil {
		return err
	}
	g, err := o.github()
	if err != nil {
		return err
	}
	b, err := os.ReadFile(*o.changelog)
	if err != nil {
		return err
	}
	body, err := release.Notes(b, *o.version)
	if err != nil {
		return err
	}
	tag := "v" + *o.version
	files := append([]string{release.SumsName, release.SigName, release.InfoName}, assetNames(*o.version)...)
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(*o.dir, f)); err != nil {
			return err
		}
	}
	r, err := g.ReleaseByTag(tag)
	if err != nil {
		return err
	}
	if r != nil && !r.Draft {
		return fmt.Errorf("release %s is already published", tag)
	}
	if r == nil {
		if r, err = g.CreateDraft(tag, *o.commit, "Download Center "+*o.version, body, release.Prerelease(*o.version)); err != nil {
			return err
		}
		fmt.Printf("created draft release %s\n", tag)
	} else {
		fmt.Printf("refilling the existing draft release %s\n", tag)
	}
	for _, f := range files {
		if a := r.Asset(f); a != nil {
			if err := g.DeleteAsset(a); err != nil {
				return err
			}
		}
		fmt.Printf("  uploading %s\n", f)
		if err := g.Upload(r, f, filepath.Join(*o.dir, f)); err != nil {
			return err
		}
	}
	fmt.Println(r.HTMLURL)
	return nil
}

func assetNames(version string) []string {
	var out []string
	for _, a := range release.Arches {
		out = append(out, release.AssetName(version, a))
	}
	return out
}

func (o *opts) draftRelease(g *release.GitHub) (*release.Release, string, error) {
	v, err := o.versionFromTag()
	if err != nil {
		return nil, "", err
	}
	r, err := g.ReleaseByTag(*o.tag)
	if err != nil {
		return nil, "", err
	}
	if r == nil {
		return nil, "", fmt.Errorf("no release for %s: run tools/release.sh, which creates the draft before pushing the tag", *o.tag)
	}
	if !r.Draft {
		return nil, "", fmt.Errorf("release %s is already published", *o.tag)
	}
	return r, v, nil
}

func verify(args []string) error {
	o := parse("verify", args)
	if err := need(o.tag); err != nil {
		return err
	}
	g, err := o.github()
	if err != nil {
		return err
	}
	pb, err := o.publicKey()
	if err != nil {
		return err
	}
	pub, err := release.ParsePublic(pb)
	if err != nil {
		return err
	}
	r, v, err := o.draftRelease(g)
	if err != nil {
		return err
	}
	if _, err := g.CheckRelease(r, v, pub); err != nil {
		return err
	}
	fmt.Printf("draft %s: %d packages, list and signature (key %s) check out\n", *o.tag, len(release.Arches), release.KeyID(pub))
	return nil
}

func publish(args []string) error {
	o := parse("publish", args)
	if err := need(o.tag); err != nil {
		return err
	}
	g, err := o.github()
	if err != nil {
		return err
	}
	r, v, err := o.draftRelease(g)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(*o.changelog)
	if err != nil {
		return err
	}
	n, err := release.Notes(b, v)
	if err != nil {
		return err
	}
	if err := g.Publish(r, n, release.Prerelease(v)); err != nil {
		return err
	}
	fmt.Printf("published %s\n", r.HTMLURL)
	return nil
}

func feed(args []string) error {
	o := parse("feed", args)
	if err := need(o.out); err != nil {
		return err
	}
	g, err := o.github()
	if err != nil {
		return err
	}
	pb, err := o.publicKey()
	if err != nil {
		return err
	}
	pub, err := release.ParsePublic(pb)
	if err != nil {
		return err
	}
	f, skipped, err := g.BuildFeed(pub)
	if err != nil {
		return err
	}
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, "left out "+s)
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	if err := os.WriteFile(*o.out, append(b, '\n'), 0644); err != nil {
		return err
	}
	fmt.Printf("%s: %d releases\n", *o.out, len(f.Releases))
	return nil
}
