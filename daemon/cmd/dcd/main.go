// dcd is the Download Center control daemon: HTTP server for the UI, the
// REST API, the event stream and the V4-compatible API; queue, schedule and
// events; supervisor of the download engines.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"downloadcenter/internal/api"
	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
	"downloadcenter/internal/engine/dl"
	"downloadcenter/internal/qts"
	"downloadcenter/internal/store"
)

// Version is set at build time (-ldflags "-X main.Version=...").
var Version = "dev"

func main() {
	log.SetFlags(log.LstdFlags)
	// Invoked as ds.cgi through the Qdownload symlink: forward to the daemon
	if strings.HasSuffix(os.Args[0], ".cgi") || os.Getenv("GATEWAY_INTERFACE") != "" {
		os.Exit(runCGI())
	}
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "check-sid":
		if len(os.Args) < 3 {
			usage()
		}
		a, err := qts.ValidateSID(os.Args[2], "", "")
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		for k := range a {
			fmt.Println(k)
		}
		fmt.Println("authPassed =", a["authPassed"], "user =", a.User(), "isAdmin =", a["isAdmin"])
	case "version":
		fmt.Println(Version)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: dcd serve -root <QPKG_ROOT> [-port N] | dcd check-sid <sid> | dcd version")
	os.Exit(2)
}

func readPort(path string, def int) int {
	b, err := os.ReadFile(path)
	if err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 1024 && n < 65536 {
			return n
		}
	}
	os.WriteFile(path, []byte(strconv.Itoa(def)+"\n"), 0600)
	return def
}

// removeAria2 cleans up after versions that ran aria2 (up to 0.9.0): those
// kept aria2c running across dcd restarts, and a package update leaves
// files the new package no longer has.
func removeAria2(root, data string) {
	pidFile := filepath.Join(data, "run", "aria2.pid")
	if b, err := os.ReadFile(pidFile); err == nil {
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(b))); pid > 1 {
			if cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); strings.Contains(string(cmdline), "aria2c") {
				log.Printf("stopping aria2c (pid %d) left by an earlier version", pid)
				syscall.Kill(pid, syscall.SIGTERM)
				for i := 0; i < 50; i++ {
					if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err != nil {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
	for _, p := range []string{pidFile, filepath.Join(root, "bin", "aria2c"),
		filepath.Join(data, "aria2.conf"), filepath.Join(data, "aria2.secret"), filepath.Join(data, "aria2_port"),
		filepath.Join(data, "aria2.dht"), filepath.Join(data, "logs", "aria2.log"), filepath.Join(data, "logs", "aria2.out")} {
		os.Remove(p)
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	root := fs.String("root", "", "QPKG install path")
	port := fs.Int("port", 0, "HTTP port on 127.0.0.1")
	devUser := fs.String("dev-user", "", "development only: direct loopback requests act as this user")
	fs.Parse(args)
	if *root == "" {
		usage()
	}
	// Downloads are group-writable like in shared folders (also inherited
	// by dc-bt); data/ itself stays private through explicit modes
	syscall.Umask(0002)
	data := filepath.Join(*root, "data")
	os.MkdirAll(filepath.Join(data, "run"), 0700)
	os.MkdirAll(filepath.Join(data, "logs"), 0700)
	os.Chmod(data, 0700)
	if *port == 0 {
		*port = readPort(filepath.Join(data, "http_port"), 18780)
	}
	log.Printf("dcd %s starting (root %s, port %d)", Version, *root, *port)

	db, err := store.Open(filepath.Join(data, "dc.db"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	m := core.New(db, data)

	// Engines: libcurl (dc-dl) for URLs, libtorrent (dc-bt) for torrents
	removeAria2(*root, data)
	ue := dl.New(filepath.Join(*root, "bin", "dc-dl"), data)
	ue.Logf = m.Log
	if err := ue.Start(); err != nil {
		log.Printf("url engine: %v (URLs cannot be downloaded until dc-dl runs)", err)
	}
	m.URL = ue
	startOptional(m, *root, data)

	if err := m.Start(); err != nil {
		log.Fatalf("manager: %v", err)
	}
	au := auth.New(db)
	srv := api.New(m, au, filepath.Join(*root, "web"), Version)
	api.SetOfficialCheck(func() (bool, bool) { return qts.QPKGInstalled("DownloadStation") })
	srv.DevUser = *devUser
	if *devUser != "" {
		log.Printf("WARNING: development mode, loopback requests act as %s", *devUser)
	}
	registerExtensions(srv, m, au, *root, data)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()
	os.WriteFile(filepath.Join(data, "run", "ready"), []byte(strconv.Itoa(os.Getpid())), 0600)
	log.Printf("dcd ready")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Printf("dcd stopping")
	os.Remove(filepath.Join(data, "run", "ready"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	hs.Shutdown(ctx)
	cancel()
	stopExtensions()
	m.Stop()
	ue.Stop()
	for _, e := range m.Engines {
		e.Stop()
	}
	db.Close()
	log.Printf("dcd stopped")
}
