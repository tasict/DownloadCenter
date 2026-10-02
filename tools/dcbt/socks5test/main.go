// socks5test is a minimal logging SOCKS5 server (no auth, CONNECT only)
// used to check that dc-bt sends tracker and peer connections through the
// proxy. Development tool; not shipped.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync/atomic"
)

var conns int64

func main() {
	addr := flag.String("listen", "127.0.0.1:16969", "listen address")
	flag.Parse()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("socks5test listening on %s", *addr)
	for {
		c, err := ln.Accept()
		if err != nil {
			continue
		}
		go handle(c)
	}
}

func handle(c net.Conn) {
	defer c.Close()
	h := make([]byte, 2)
	if _, err := io.ReadFull(c, h); err != nil || h[0] != 5 {
		return
	}
	io.CopyN(io.Discard, c, int64(h[1]))
	c.Write([]byte{5, 0})
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		io.ReadFull(c, b)
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		io.ReadFull(c, b)
		host = net.IP(b).String()
	case 3:
		l := make([]byte, 1)
		io.ReadFull(c, l)
		b := make([]byte, int(l[0]))
		io.ReadFull(c, b)
		host = string(b)
	}
	p := make([]byte, 2)
	io.ReadFull(c, p)
	port := binary.BigEndian.Uint16(p)
	target := net.JoinHostPort(host, strconv.Itoa(int(port)))
	if req[1] != 1 {
		fmt.Printf("REQ cmd=%d %s (refused)\n", req[1], target)
		c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	n := atomic.AddInt64(&conns, 1)
	fmt.Printf("CONNECT #%d %s\n", n, target)
	up, err := net.Dial("tcp", target)
	if err != nil {
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	go io.Copy(up, c)
	io.Copy(c, up)
}
