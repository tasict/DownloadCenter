//go:build linux

package phpbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// The helper refuses to do anything outside new namespaces: started
// directly, it mounts nothing and runs nothing.
func TestHelperOutsideNamespaces(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "php")
	os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	root := filepath.Join(dir, "root")
	os.Mkdir(root, 0o700)
	cfg, _ := json.Marshal(map[string]any{"root": root, "php": script, "ext_dir": dir, "shim": "probe.php", "wall": 5,
		"uid": slotUID(0), "memory": memoryLimit})
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	parent, child := os.NewFile(uintptr(fds[0]), "p"), os.NewFile(uintptr(fds[1]), "c")
	ar, aw, _ := os.Pipe()
	defer ar.Close()
	cmd := exec.Command("/proc/self/exe", HelperArg)
	cmd.Stdin = bytes.NewReader(cfg)
	cmd.ExtraFiles = []*os.File{child, aw}
	before, _ := os.ReadFile("/proc/self/mounts")
	err = cmd.Run()
	child.Close()
	aw.Close()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Errorf("helper: %v", err)
	}
	c, _ := net.FileConn(parent)
	parent.Close()
	buf := make([]byte, 512)
	n, _ := c.Read(buf)
	c.Close()
	if !bytes.Contains(buf[:n], []byte("not the first process")) {
		t.Errorf("status %q", buf[:n])
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the program ran")
	}
	if after, _ := os.ReadFile("/proc/self/mounts"); !bytes.Equal(before, after) {
		t.Error("mounts changed")
	}
}
