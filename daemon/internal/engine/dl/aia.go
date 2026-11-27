package dl

// Intermediate certificates a server leaves out. Browsers fetch them from
// the address the server's certificate names (Authority Information Access,
// caIssuers) or keep them from earlier visits; libcurl does neither. dc-dl
// (1.5) reports the certificate that lacks an issuer and its caIssuers
// addresses, dcd fetches the issuer here, and dc-dl tries again with it as
// a candidate: an untrusted certificate that may complete the chain, never
// an authority itself, so the chain still has to end at one the NAS trusts.

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"downloadcenter/internal/netutil"
)

const (
	maxChainLevels = 3                // issuers fetched for one request
	aiaMaxBytes    = 64 << 10         // a certificate or a few in PKCS#7
	aiaTimeout     = 10 * time.Second // one fetch
	aiaFailFor     = 10 * time.Minute // an address that gave nothing usable is not asked again
	aiaKeep        = 64               // issuers kept
	aiaHostKeep    = 4                // candidates sent to one host
	aiaMaxHosts    = 256              // hosts remembered
	aiaMaxCerts    = 16               // certificates read from one answer
)

// needIssuer is what dc-dl reports about a certificate without an issuer.
type needIssuer struct {
	Cert string   `json:"cert"` // base64 DER
	URLs []string `json:"urls"` // its caIssuers addresses
}

// strictAll: an address a server's certificate names is fetched only from
// public unicast addresses at ports 80 and 443, whoever's task asked.
var strictAll = &netutil.Rule{Strict: func(string) bool { return true }}

// chains keeps the issuers fetched (by the key that names them in the
// certificates they signed) and, per host, the candidates its requests
// carry.
type chains struct {
	// fetch gets one caIssuers address through proxy ("" = direct; tests
	// replace it).
	fetch func(ctx context.Context, rawURL, proxy string) ([]byte, error)
	// proxy is the proxy URL for an address ("" = direct; an error: do not
	// connect), the rule of a regular user's request to it. It applies when
	// the task connects directly; a task's own proxy is always used.
	proxy func(rawURL string) (string, error)

	mu     sync.Mutex
	byKey  map[string][]byte // issuer key -> DER
	keys   []string          // oldest first
	byHost map[string][]string
	hosts  []string             // oldest first
	failed map[string]time.Time // address -> not before
	flight map[string]*flight
}

type flight struct {
	done chan struct{}
	der  []byte
}

func newChains() *chains {
	c := &chains{byKey: map[string][]byte{}, byHost: map[string][]string{}, failed: map[string]time.Time{}, flight: map[string]*flight{}}
	c.fetch = c.get
	return c
}

// forHost returns the candidates (base64 DER) requests to host carry.
func (c *chains) forHost(host string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.byHost[strings.ToLower(host)]...)
}

// issuerKey names the issuer of cert: its authority key id, else the
// issuer's name.
func issuerKey(cert *x509.Certificate) string {
	if len(cert.AuthorityKeyId) > 0 {
		return "k:" + hex.EncodeToString(cert.AuthorityKeyId)
	}
	sum := sha256.Sum256(cert.RawIssuer)
	return "n:" + hex.EncodeToString(sum[:])
}

// complete finds the issuer of the certificate dc-dl reported (kept, or
// fetched from its caIssuers addresses) and adds it to host's candidates.
// taskProxy is the proxy of the request that needed it: a fetch is never
// less proxied than the download (its server would learn the NAS's
// address). It reports whether it has one.
func (c *chains) complete(ctx context.Context, host string, ni *needIssuer, taskProxy string) bool {
	raw, err := base64.StdEncoding.DecodeString(ni.Cert)
	if err != nil {
		return false
	}
	child, err := x509.ParseCertificate(raw)
	if err != nil {
		return false
	}
	key := issuerKey(child)
	for {
		c.mu.Lock()
		if der := c.byKey[key]; der != nil {
			c.addHost(host, der)
			c.mu.Unlock()
			return true
		}
		f := c.flight[key]
		if f == nil {
			f = &flight{done: make(chan struct{})}
			c.flight[key] = f
			c.mu.Unlock()
			f.der = c.find(ctx, child, ni.URLs, taskProxy)
			c.mu.Lock()
			delete(c.flight, key)
			if f.der != nil {
				c.keep(key, f.der)
				c.addHost(host, f.der)
			}
			c.mu.Unlock()
			close(f.done)
			return f.der != nil
		}
		c.mu.Unlock()
		// Another connection is fetching it: take its result
		select {
		case <-f.done:
		case <-ctx.Done():
			return false
		}
		if f.der == nil {
			return false
		}
	}
}

// find fetches the issuer of child from its addresses: a CA certificate
// whose key made child's signature. Anything else is left out.
func (c *chains) find(ctx context.Context, child *x509.Certificate, urls []string, taskProxy string) []byte {
	for _, u := range urls {
		if !strings.HasPrefix(strings.ToLower(u), "http://") {
			continue
		}
		c.mu.Lock()
		until, bad := c.failed[u]
		c.mu.Unlock()
		if bad && time.Now().Before(until) {
			continue
		}
		proxy := taskProxy
		if proxy == "" && c.proxy != nil {
			var err error
			if proxy, err = c.proxy(u); err != nil {
				continue // a proxy is required and none applies: no fetch
			}
		}
		body, err := c.fetch(ctx, u, proxy)
		if err == nil {
			for _, cand := range parseCerts(body) {
				if cand.IsCA && cand.BasicConstraintsValid && child.CheckSignatureFrom(cand) == nil {
					return cand.Raw
				}
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		c.mu.Lock()
		c.failed[u] = time.Now().Add(aiaFailFor)
		for k, t := range c.failed {
			if time.Now().After(t) {
				delete(c.failed, k)
			}
		}
		c.mu.Unlock()
	}
	return nil
}

// keep remembers an issuer. Caller holds c.mu.
func (c *chains) keep(key string, der []byte) {
	if _, ok := c.byKey[key]; !ok {
		c.keys = append(c.keys, key)
	}
	c.byKey[key] = der
	for len(c.keys) > aiaKeep {
		delete(c.byKey, c.keys[0])
		c.keys = c.keys[1:]
	}
}

// addHost adds a candidate for host. Caller holds c.mu.
func (c *chains) addHost(host string, der []byte) {
	host = strings.ToLower(host)
	b := base64.StdEncoding.EncodeToString(der)
	list, ok := c.byHost[host]
	if !ok {
		c.hosts = append(c.hosts, host)
	}
	for _, x := range list {
		if x == b {
			return
		}
	}
	list = append(list, b)
	if len(list) > aiaHostKeep {
		list = list[len(list)-aiaHostKeep:]
	}
	c.byHost[host] = list
	for len(c.hosts) > aiaMaxHosts {
		delete(c.byHost, c.hosts[0])
		c.hosts = c.hosts[1:]
	}
}

// get fetches one caIssuers address: http only, public unicast addresses at
// ports 80 and 443 (also behind a name), through proxy ("" = direct), no
// redirects, no cookies, bounded.
func (c *chains) get(ctx context.Context, raw, proxy string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil {
		return nil, errors.New("not an http address")
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	if err := strictAll.CheckLiteral(u.Hostname(), port); err != nil {
		return nil, err
	}
	rt := netutil.TransportRule(aiaTimeout, proxy, strictAll)
	if t, ok := rt.(interface{ CloseIdleConnections() }); ok {
		defer t.CloseIdleConnections()
	}
	cl := &http.Client{Transport: rt, Timeout: aiaTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(ctx, aiaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "DownloadCenter/1.0")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, aiaMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > aiaMaxBytes {
		return nil, errors.New("too large")
	}
	return body, nil
}

// parseCerts reads the certificates of a caIssuers answer: DER, PEM or a
// PKCS#7 bundle (.p7c), at most aiaMaxCerts.
func parseCerts(b []byte) []*x509.Certificate {
	var out []*x509.Certificate
	if strings.HasPrefix(strings.TrimSpace(string(b[:min(len(b), 64)])), "-----BEGIN") {
		for rest := b; len(out) < aiaMaxCerts; {
			var blk *pem.Block
			if blk, rest = pem.Decode(rest); blk == nil {
				break
			}
			if blk.Type != "CERTIFICATE" {
				continue
			}
			if c, err := x509.ParseCertificate(blk.Bytes); err == nil {
				out = append(out, c)
			}
		}
		return out
	}
	if c, err := x509.ParseCertificate(b); err == nil {
		return []*x509.Certificate{c}
	}
	certs, _ := pkcs7Certs(b)
	if len(certs) > aiaMaxCerts {
		certs = certs[:aiaMaxCerts]
	}
	return certs
}

var oidSignedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}

// pkcs7Certs reads the certificates of a PKCS#7 SignedData (RFC 2315): a
// ContentInfo whose content holds them in its [0] IMPLICIT SET.
func pkcs7Certs(b []byte) ([]*x509.Certificate, error) {
	// For a RawValue the explicit [0] is not unwrapped: Bytes is the
	// SignedData inside it
	var ci struct {
		ContentType asn1.ObjectIdentifier
		Content     asn1.RawValue `asn1:"explicit,tag:0"`
	}
	if rest, err := asn1.Unmarshal(b, &ci); err != nil {
		return nil, err
	} else if len(rest) > 0 {
		return nil, errors.New("trailing data")
	}
	if !ci.ContentType.Equal(oidSignedData) {
		return nil, errors.New("not signed data")
	}
	var sd struct {
		Version          int
		DigestAlgorithms asn1.RawValue
		ContentInfo      asn1.RawValue
		Certificates     asn1.RawValue `asn1:"optional,tag:0"`
		CRLs             asn1.RawValue `asn1:"optional,tag:1"`
		SignerInfos      asn1.RawValue
	}
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return nil, err
	}
	if len(sd.Certificates.Bytes) == 0 {
		return nil, errors.New("no certificates")
	}
	var out []*x509.Certificate
	for rest := sd.Certificates.Bytes; len(rest) > 0 && len(out) < aiaMaxCerts; {
		var raw asn1.RawValue
		var err error
		if rest, err = asn1.Unmarshal(rest, &raw); err != nil {
			break
		}
		if c, err := x509.ParseCertificate(raw.FullBytes); err == nil {
			out = append(out, c)
		}
	}
	return out, nil
}
