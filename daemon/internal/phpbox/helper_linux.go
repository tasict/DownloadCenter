//go:build linux

package phpbox

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// The helper is dcd started again as the first process of new network,
// mount, PID, IPC and UTS namespaces. As root in them it builds the box's
// root, starts the relay's entrance and runs PHP as its slot's uid (see
// slotUID), under the syscall filter and its memory limit; it never runs
// anything but that PHP binary, with a fixed argument vector. Its file
// descriptors: 0 the configuration, 1 and 2 PHP's output, 3 the control
// socket to dcd, 4 the answer pipe (PHP's fd 3).

const (
	relayPort = "3128"
	// Linux values, the same on amd64, arm64 and arm
	rlimitNproc     = 6
	rlimitAS        = 9
	prSetNoNewPrivs = 38
	iffUp           = 0x1
)

// disabled are the PHP functions a plugin may not call: starting programs,
// opening sockets itself, signals and identity changes, mail, links,
// loading code and changing the environment or time limit.
var disabled = []string{
	"exec", "shell_exec", "system", "passthru", "proc_open", "popen", "proc_close", "proc_get_status", "proc_nice",
	"proc_terminate", "pcntl_exec", "pcntl_fork", "dl", "mail", "mb_send_mail", "putenv", "set_time_limit",
	"fsockopen", "pfsockopen", "stream_socket_client", "stream_socket_server", "stream_socket_pair",
	"stream_socket_accept", "stream_socket_recvfrom", "stream_socket_sendto",
	"posix_kill", "posix_setuid", "posix_setgid", "posix_seteuid", "posix_setegid", "posix_setsid", "posix_setpgid",
	"posix_setrlimit", "posix_mkfifo", "posix_mknod", "posix_initgroups",
	"link", "symlink", "chown", "chgrp", "lchown", "lchgrp", "openlog", "syslog",
}

// phpArgs is PHP's fixed argument vector: no php.ini (-n), only the
// extensions listed, our settings. The self-check (no open_basedir) may
// make a FIFO, to see the syscall filter refuse it.
func phpArgs(c *helperConfig) []string {
	a := []string{"php", "-n", "-d", "extension_dir=" + c.ExtDir}
	for _, e := range c.Exts {
		a = append(a, "-d", "extension="+e)
	}
	basedir := "/plugin:/host:/tmp"
	off := disabled
	if !c.Basedir {
		basedir = ""
		off = nil
		for _, f := range disabled {
			if f != "posix_mkfifo" {
				off = append(off, f)
			}
		}
	}
	if c.CAFile != "" {
		// Handles a plugin makes itself trust the same authorities
		a = append(a, "-d", "curl.cainfo="+boxCAFile, "-d", "openssl.cafile="+boxCAFile)
	}
	for _, s := range []string{
		"disable_functions=" + strings.Join(off, ","),
		"open_basedir=" + basedir,
		"memory_limit=128M",
		"max_execution_time=" + fmt.Sprint(max(c.Wall-5, 5)),
		"display_errors=0", "display_startup_errors=0", "log_errors=1", "html_errors=0", "error_reporting=32767",
		"allow_url_fopen=0", "allow_url_include=0", "enable_dl=0", "file_uploads=0",
		"auto_prepend_file=", "auto_append_file=", "include_path=.:/host",
		"date.timezone=UTC", "default_socket_timeout=20",
		"sys_temp_dir=/tmp", "upload_tmp_dir=/tmp", "session.save_path=/tmp",
		"zend.assertions=-1", "expose_php=0", "variables_order=GPCS",
	} {
		a = append(a, "-d", s)
	}
	return append(a, "-f", "/host/"+c.Shim)
}

// Helper runs the helper; dcd's main calls it first when IsHelper.
func Helper() int {
	ctl, err := controlConn()
	if err != nil {
		return 2
	}
	defer ctl.Close()
	say := func(stage string, err error) {
		st := status{Stage: stage}
		if err != nil {
			st.Err = err.Error()
		}
		b, _ := json.Marshal(st)
		ctl.Write(append([]byte{'S'}, b...))
	}
	c, err := readConfig()
	if err == nil {
		err = isolated()
	}
	var ln net.Listener
	if err == nil {
		ln, err = setup(c)
	}
	if err != nil {
		say("setup", err)
		return 3
	}
	go relayEntrance(ln, ctl)
	stage, err := runPHP(c, func() { say("exec", nil) })
	if err != nil {
		say(stage, err)
		return 4
	}
	say("done", nil)
	return 0
}

func controlConn() (*net.UnixConn, error) {
	syscall.CloseOnExec(3)
	syscall.CloseOnExec(4)
	f := os.NewFile(3, "control")
	c, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	u, ok := c.(*net.UnixConn)
	if !ok {
		c.Close()
		return nil, errors.New("control is not a unix socket")
	}
	return u, nil
}

func readConfig() (*helperConfig, error) {
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if err != nil {
		return nil, err
	}
	var c helperConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(c.Root) || !filepath.IsAbs(c.PHP) || !filepath.IsAbs(c.ExtDir) || path.Base(c.Shim) != c.Shim ||
		c.UID < minUID || c.UID > boxUID || c.Memory < 64<<20 || c.CAFile != "" && !filepath.IsAbs(c.CAFile) {
		return nil, errors.New("bad configuration")
	}
	return &c, nil
}

// isolated refuses to go on unless this process really is the first of a
// new PID namespace, in its own mount namespace and a network namespace
// with nothing but lo: never mount anything on the host.
func isolated() error {
	if os.Getpid() != 1 {
		return errors.New("not the first process of a PID namespace")
	}
	// /proc is still the host's here: /proc/1 is the host's first process
	self, err1 := os.Readlink("/proc/self/ns/mnt")
	host, err2 := os.Readlink("/proc/1/ns/mnt")
	if err1 != nil || err2 != nil || self == host {
		return errors.New("no mount namespace of its own")
	}
	ifs, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, i := range ifs {
		if i.Name != "lo" {
			return errors.New("network namespace has other interfaces")
		}
	}
	return nil
}

func mount(src, dst, fstype string, flags uintptr, data string) error {
	if err := syscall.Mount(src, dst, fstype, flags, data); err != nil {
		return fmt.Errorf("mount %s: %w", dst, err)
	}
	return nil
}

// setup builds the root and opens the relay's entrance.
func setup(c *helperConfig) (net.Listener, error) {
	syscall.Sethostname([]byte("sandbox"))
	// Nothing done here may reach the host's mounts
	if err := mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return nil, err
	}
	root := c.Root
	if err := mount("tmpfs", root, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "size=32m,nr_inodes=4096,mode=0755"); err != nil {
		return nil, err
	}
	in := func(p string) string { return filepath.Join(root, p) }
	for _, d := range []string{"dev", "proc", "tmp", "plugin", "host", "etc"} {
		if err := os.MkdirAll(in(d), 0o755); err != nil {
			return nil, err
		}
	}
	// System directories, read-only at the same paths
	binds := append([]string(nil), c.Binds...)
	sort.Strings(binds)
	for _, b := range binds {
		if err := bindRO(b, in(b), true); err != nil {
			return nil, err
		}
	}
	for _, l := range c.Links {
		if _, err := os.Lstat(in(l[0])); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(in(l[0])), 0o755); err != nil {
			return nil, err
		}
		if err := os.Symlink(l[1], in(l[0])); err != nil {
			return nil, err
		}
	}
	// The devices PHP and its libraries use
	for _, d := range []string{"null", "zero", "urandom"} {
		f, err := os.OpenFile(in("dev/"+d), os.O_CREATE|os.O_WRONLY, 0o666)
		if err != nil {
			return nil, err
		}
		f.Close()
		if err := mount("/dev/"+d, in("dev/"+d), "", syscall.MS_BIND, ""); err != nil {
			return nil, err
		}
		mount("", in("dev/"+d), "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_NOSUID|syscall.MS_NOEXEC, "")
	}
	// The plugin's files and our PHP, readable by the box's uid
	if c.PluginDir != "" {
		if err := copyPlugin(c.PluginDir, in("plugin")); err != nil {
			return nil, err
		}
	}
	entries, err := fs.ReadDir(shims, "shim")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		b, _ := shims.ReadFile("shim/" + e.Name())
		if err := os.WriteFile(in("host/"+e.Name()), b, 0o444); err != nil {
			return nil, err
		}
	}
	// The merged certificate authorities PHP's curl trusts
	if c.CAFile != "" {
		f, err := os.Open(c.CAFile)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(f, maxCAFile))
		f.Close()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(in(boxCAFile), b, 0o444); err != nil {
			return nil, err
		}
	}
	// A /proc of the new PID namespace that shows a process only to its
	// owner
	if err := mount("proc", in("proc"), "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "hidepid=2"); err != nil {
		if err := mount("proc", in("proc"), "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
			return nil, err
		}
	}
	if err := mount("tmpfs", in("tmp"), "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "size=16m,nr_inodes=1024,mode=1777"); err != nil {
		return nil, err
	}
	// The root itself becomes read-only
	if err := mount("", root, "", syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
		return nil, err
	}
	if err := loUp(); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+relayPort)
	if err != nil {
		return nil, err
	}
	if err := syscall.Chroot(root); err != nil {
		ln.Close()
		return nil, err
	}
	if err := os.Chdir("/plugin"); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// bindRO binds src at dst read-only, without the mounts below src.
func bindRO(src, dst string, dir bool) error {
	if dir {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
	}
	if err := mount(src, dst, "", syscall.MS_BIND, ""); err != nil {
		return err
	}
	return mount("", dst, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NODEV, "")
}

// copyPlugin copies the plugin's regular files (dcd's own, 0600) into the
// box, readable by everyone there; links and anything else are skipped.
func copyPlugin(src, dst string) error {
	n, total := 0, int64(0)
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		if n++; n > MaxEntries {
			return errors.New("too many files")
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.Mkdir(target, 0o555)
		case d.Type().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if total += int64(len(b)); total > MaxUnpacked {
				return errors.New("too large")
			}
			return os.WriteFile(target, b, 0o444)
		}
		return nil
	})
}

// loUp brings the loopback interface of the namespace up.
func loUp() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	// struct ifreq: the name, then the flags (a short; the rest is padding)
	var ifr [40]byte
	copy(ifr[:], "lo")
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&ifr[0]))); e != 0 {
		return e
	}
	flags := binary.LittleEndian.Uint16(ifr[16:]) | iffUp
	binary.LittleEndian.PutUint16(ifr[16:], flags)
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifr[0]))); e != 0 {
		return e
	}
	return nil
}

// relayEntrance hands every connection the box opens to 127.0.0.1 to dcd,
// as a file descriptor on the control socket.
func relayEntrance(ln net.Listener, ctl *net.UnixConn) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		f, err := c.(*net.TCPConn).File()
		c.Close()
		if err != nil {
			continue
		}
		ctl.WriteMsgUnix([]byte{'C'}, syscall.UnixRights(int(f.Fd())), nil)
		f.Close()
	}
}

// runPHP runs PHP as the slot's uid with no groups, its limits, no new
// privileges and the syscall filter, the request on stdin and the answer
// pipe as fd 3, at most c.Wall seconds. Its memory limit is set on the
// PHP process before the request is written: the shims read the whole
// request before they load anything of the plugin. A failure to filter or
// limit is reported as the stage "filter": then nothing runs.
func runPHP(c *helperConfig, started func()) (string, error) {
	for _, l := range []struct {
		res      int
		cur, max uint64
	}{
		{syscall.RLIMIT_CPU, uint64(c.Wall), uint64(c.Wall) + 2},
		{syscall.RLIMIT_FSIZE, 16 << 20, 16 << 20},
		{syscall.RLIMIT_NOFILE, 64, 64},
		{syscall.RLIMIT_CORE, 0, 0},
		// PHP alone: no thread, no other process (curl only starts a
		// thread to look up a name itself, and the relay is an address).
		// The memory limit is per process, so this keeps a slot to one.
		{rlimitNproc, 1, 1},
	} {
		if err := syscall.Setrlimit(l.res, &syscall.Rlimit{Cur: l.cur, Max: l.max}); err != nil {
			return "exec", fmt.Errorf("limit %d: %w", l.res, err)
		}
	}
	// no_new_privs and the syscall filter belong to a thread and go to
	// what it starts: set them on the thread that starts PHP. The helper
	// has done everything the filter refuses by now.
	runtime.LockOSThread()
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0); e != 0 {
		return "filter", fmt.Errorf("no_new_privs: %w", e)
	}
	if err := installFilter(); err != nil {
		return "filter", err
	}
	stdin, w, err := os.Pipe()
	if err != nil {
		return "exec", err
	}
	answer := os.NewFile(4, "answer")
	cmd := &exec.Cmd{
		Path:       c.PHP,
		Args:       phpArgs(c),
		Env:        []string{"http_proxy=http://127.0.0.1:" + relayPort, "https_proxy=http://127.0.0.1:" + relayPort, "HOME=/tmp", "TMPDIR=/tmp", "PATH=/nonexistent"},
		Dir:        "/plugin",
		Stdin:      stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		ExtraFiles: []*os.File{answer},
		SysProcAttr: &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: c.UID, Gid: c.UID, Groups: []uint32{}},
			Setsid:     true,
		},
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		w.Close()
		return "exec", err
	}
	stdin.Close()
	answer.Close()
	// The memory limit of PHP alone (the helper's Go runtime reserves more
	// address space than that): PHP waits for the request meanwhile
	lim := [2]uint64{c.Memory, c.Memory}
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRLIMIT64, uintptr(cmd.Process.Pid), rlimitAS, uintptr(unsafe.Pointer(&lim)), 0, 0, 0); e != 0 && e != syscall.ESRCH {
		cmd.Process.Kill()
		cmd.Wait()
		w.Close()
		return "filter", fmt.Errorf("memory limit: %w", e)
	}
	started()
	go func() {
		w.Write(c.Request)
		w.Close()
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(time.Duration(c.Wall) * time.Second):
		cmd.Process.Kill()
		<-done
	}
	return "", nil
}
