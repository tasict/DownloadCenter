package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/analytics"
	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/engine/lt"
	"downloadcenter/internal/hosters"
	"downloadcenter/internal/importer"
	"downloadcenter/internal/notify"
	"downloadcenter/internal/update"
	"downloadcenter/internal/v4"
)

var stoppers []func()

// startOptional starts engines that are present in this build: the
// libtorrent sidecar when bin/dc-bt exists.
func startOptional(m *core.Manager, root, data string) {
	bin := filepath.Join(root, "bin", "dc-bt")
	if !lt.Available(bin) {
		return
	}
	e := lt.New(bin, data)
	if e == nil {
		return
	}
	if err := e.ApplyGlobal(m.GlobalFor("libtorrent")); err != nil && !strings.Contains(err.Error(), "restart required") {
		log.Printf("libtorrent: %v", err)
	}
	if err := e.Start(); err != nil {
		log.Printf("libtorrent: %v (will retry)", err)
	}
	m.Engines["libtorrent"] = e
}

// registerExtensions wires the optional packages into the server.
func registerExtensions(srv *api.Server, m *core.Manager, au *auth.Service, root, data string) {
	hs := hosters.New(m)
	m.Hosters = hs
	api.Verifier = hs
	v4.Register(srv, m, au, root, data)
	importer.Register(srv, m, au)
	stoppers = append(stoppers, notify.Register(srv, m, au, data))
	stoppers = append(stoppers, update.Register(srv, m, root, data))
	stoppers = append(stoppers, analytics.Register(srv, m, root, data))
}

func stopExtensions() {
	for i := len(stoppers) - 1; i >= 0; i-- {
		stoppers[i]()
	}
}

// runCGI forwards a CGI request (the firmware's /downloadstation/V4/...
// route through the Qdownload symlink) to the daemon on loopback.
func runCGI() int {
	root := filepath.Dir(filepath.Dir(mustExe()))
	port := 18780
	if b, err := os.ReadFile(filepath.Join(root, "data", "http_port")); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			port = n
		}
	}
	uri := os.Getenv("REQUEST_URI")
	if uri == "" {
		uri = os.Getenv("SCRIPT_URL")
	}
	path, query, _ := strings.Cut(uri, "?")
	if q := os.Getenv("QUERY_STRING"); q != "" {
		query = q
	}
	target := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	if query != "" {
		target += "?" + query
	}
	method := os.Getenv("REQUEST_METHOD")
	if method == "" {
		method = "GET"
	}
	var body io.Reader
	if n, _ := strconv.Atoi(os.Getenv("CONTENT_LENGTH")); n > 0 {
		body = io.LimitReader(os.Stdin, int64(n))
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		cgiError(500)
		return 0
	}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "HTTP_") {
			h := strings.ReplaceAll(strings.Title(strings.ToLower(strings.ReplaceAll(k[5:], "_", " "))), " ", "-")
			if h == "Host" || h == "Connection" {
				continue
			}
			req.Header.Set(h, v)
		}
	}
	if ct := os.Getenv("CONTENT_TYPE"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("X-DC-Remote", os.Getenv("REMOTE_ADDR"))
	req.Header.Set("X-DC-Scheme", os.Getenv("REQUEST_SCHEME"))
	req.Header.Set("X-DC-Via", "cgi")
	req.Header.Set("X-Forwarded-Host", os.Getenv("HTTP_HOST"))
	cl := &http.Client{Timeout: 300 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := cl.Do(req)
	if err != nil {
		cgiError(502)
		return 0
	}
	defer resp.Body.Close()
	w := bufio.NewWriter(os.Stdout)
	fmt.Fprintf(w, "Status: %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	for k, vs := range resp.Header {
		for _, v := range vs {
			fmt.Fprintf(w, "%s: %s\r\n", k, v)
		}
	}
	fmt.Fprint(w, "\r\n")
	io.Copy(w, resp.Body)
	w.Flush()
	return 0
}

func cgiError(code int) {
	fmt.Printf("Status: %d %s\r\nContent-Type: application/json\r\n\r\n{\"error\":%d}\n", code, http.StatusText(code), 8)
}

func mustExe() string {
	p, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
