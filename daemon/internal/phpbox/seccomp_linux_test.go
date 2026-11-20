//go:build linux

package phpbox

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// runBPF runs a seccomp program on one call (nr, arch, first argument) the
// way the kernel's interpreter does for the instructions the program uses.
func runBPF(t *testing.T, prog []sockFilter, nr, arch uint32, arg0 uint64) uint32 {
	t.Helper()
	return runBPF3(t, prog, nr, arch, arg0, 0, 0)
}

// runBPF3 is runBPF with the first three arguments.
func runBPF3(t *testing.T, prog []sockFilter, nr, arch uint32, arg0, arg1, arg2 uint64) uint32 {
	t.Helper()
	data := make([]byte, 64)
	binary.LittleEndian.PutUint32(data[offNr:], nr)
	binary.LittleEndian.PutUint32(data[offArch:], arch)
	binary.LittleEndian.PutUint64(data[16:], arg0)
	binary.LittleEndian.PutUint64(data[24:], arg1)
	binary.LittleEndian.PutUint64(data[32:], arg2)
	var a uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.Code {
		case bpfLdWAbs:
			if in.K%4 != 0 || in.K+4 > uint32(len(data)) {
				t.Fatalf("load at %d out of seccomp_data", in.K)
			}
			a = binary.LittleEndian.Uint32(data[in.K:])
		case 0x54: // BPF_ALU | BPF_AND | BPF_K
			a &= in.K
		case bpfJeqK, bpfJgeK, bpfJsetK:
			var hit bool
			switch in.Code {
			case bpfJeqK:
				hit = a == in.K
			case bpfJgeK:
				hit = a >= in.K
			default:
				hit = a&in.K != 0
			}
			if hit {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case bpfRetK:
			return in.K
		default:
			t.Fatalf("instruction %#x at %d", in.Code, pc)
		}
	}
	t.Fatal("ran off the end")
	return 0
}

// The program of each architecture: its structure, then what it decides
// for the calls it is about.
func TestFilterPrograms(t *testing.T) {
	for goarch, tab := range syscalls {
		t.Run(goarch, func(t *testing.T) {
			prog, err := buildFilter(goarch)
			if err != nil {
				t.Fatal(err)
			}
			// Structure: arch first, every jump inside the program, ends
			// with allow, within the kernel's 4096 instructions
			if len(prog) > 4096 || prog[0] != stmt(bpfLdWAbs, offArch) || prog[1].K != tab.arch || prog[2] != stmt(bpfRetK, retKillProcess) {
				t.Fatalf("head %v", prog[:3])
			}
			if prog[len(prog)-1] != stmt(bpfRetK, retAllow) {
				t.Error("does not end with allow")
			}
			for i, in := range prog {
				if in.Code != bpfRetK && in.Code != bpfLdWAbs && (i+1+int(in.Jt) >= len(prog) || i+1+int(in.Jf) >= len(prog)) {
					t.Errorf("jump out of the program at %d", i)
				}
			}
			// Every call of the table is decided by the program, and the
			// numbers are distinct
			seen := map[uint32]string{}
			for name, nr := range tab.nr {
				if other, dup := seen[nr]; dup {
					t.Errorf("%s and %s are both %d", name, other, nr)
				}
				seen[nr] = name
			}
			for _, name := range []string{"socket", "clone", "clone3", "unshare", "setns", "mount", "umount2", "pivot_root", "chroot",
				"ptrace", "process_vm_readv", "process_vm_writev", "keyctl", "add_key", "request_key", "bpf", "perf_event_open",
				"userfaultfd", "io_uring_setup", "io_uring_enter", "io_uring_register", "kexec_load", "kexec_file_load",
				"init_module", "finit_module", "delete_module", "open_by_handle_at", "mknodat", "swapon", "swapoff", "reboot",
				"settimeofday", "clock_settime", "personality"} {
				if _, ok := tab.nr[name]; !ok {
					t.Errorf("no number for %s", name)
				}
			}
			eperm, enosys, eafno := uint32(retErrno|errEPERM), uint32(retErrno|errNOSYS), uint32(retErrno|errAFNO)
			for _, name := range deniedCalls {
				if nr, ok := tab.nr[name]; ok {
					if got := runBPF(t, prog, nr, tab.arch, 0); got != eperm {
						t.Errorf("%s: %#x", name, got)
					}
				}
			}
			for _, c := range []struct {
				what string
				nr   uint32
				arg  uint64
				want uint32
			}{
				{"clone of a thread", tab.nr["clone"], 0x003d0f00, retAllow},
				{"clone of a process", tab.nr["clone"], 0x4111, retAllow},
				{"clone into a user namespace", tab.nr["clone"], 0x10000011, eperm},
				{"clone into a network namespace", tab.nr["clone"], 0x40000000 | 0x11, eperm},
				{"clone into a mount namespace", tab.nr["clone"], 0x20011, eperm},
				{"clone3", tab.nr["clone3"], 0, enosys},
				{"netlink socket", tab.nr["socket"], 16, eafno},
				{"packet socket", tab.nr["socket"], 17, eafno},
				{"unrelated call", 0x3ff, 0, retAllow},
			} {
				if got := runBPF(t, prog, c.nr, tab.arch, c.arg); got != c.want {
					t.Errorf("%s: %#x, want %#x", c.what, got, c.want)
				}
			}
			// Sockets: TCP over IPv4 and IPv6, unix stream, datagram and
			// seqpacket; the other types and protocols are refused (they can
			// make the kernel load a protocol module)
			const nonblock, cloexec = 0x800, 0x80000
			for _, c := range []struct {
				what               string
				family, typ, proto uint64
				want               uint32
			}{
				{"TCP", afInet, 1, 0, retAllow},
				{"TCP by number", afInet, 1, 6, retAllow},
				{"TCP, non-blocking, close-on-exec", afInet, 1 | nonblock | cloexec, 6, retAllow},
				{"TCP over IPv6", afInet6, 1 | cloexec, 0, retAllow},
				{"UDP", afInet, 2, 0, eperm},
				{"UDP over IPv6", afInet6, 2 | nonblock, 17, eperm},
				{"L2TP over IP", afInet, 2, 115, eperm},
				{"raw ICMP", afInet, 3, 1, eperm},
				{"SCTP", afInet6, 1, 132, eperm},
				{"MPTCP", afInet, 1, 262, eperm},
				{"stream with UDP", afInet, 1, 17, eperm},
				{"SCTP seqpacket", afInet, 5, 132, eperm},
				{"unix stream", afUnix, 1 | cloexec, 0, retAllow},
				{"unix datagram", afUnix, 2, 0, retAllow},
				{"unix seqpacket", afUnix, 5, 0, retAllow},
				{"unix with a protocol", afUnix, 1, 1, eperm},
				{"unix raw", afUnix, 3, 0, eperm},
				{"high bits of the type", afInet, 1 | 1<<20, 0, eperm},
			} {
				if got := runBPF3(t, prog, tab.nr["socket"], tab.arch, c.family, c.typ, c.proto); got != c.want {
					t.Errorf("socket %s: %#x, want %#x", c.what, got, c.want)
				}
			}
			if goarch == "amd64" {
				if got := runBPF(t, prog, tab.nr["modify_ldt"], tab.arch, 0); got != eperm {
					t.Errorf("modify_ldt: %#x", got)
				}
			}
			// Another architecture's calls end the process
			for _, other := range syscalls {
				if other.arch != tab.arch {
					if got := runBPF(t, prog, 0, other.arch, 0); got != retKillProcess {
						t.Errorf("arch %#x: %#x", other.arch, got)
					}
				}
			}
			if got := runBPF(t, prog, 0, 0x40000003, 0); got != retKillProcess {
				t.Errorf("i386: %#x", got)
			}
			if tab.x32 {
				if got := runBPF(t, prog, x32Bit|39, tab.arch, 0); got != retKillProcess {
					t.Errorf("x32: %#x", got)
				}
			}
		})
	}
	if _, err := buildFilter("mips"); !errors.Is(err, errNoFilter) {
		t.Errorf("mips: %v", err)
	}
}

// filterChild is the test binary started again to try calls under the
// real filter of this kernel (see TestMain).
const filterChild = "DC_TEST_FILTER_CHILD"

func filterChildMain() int {
	runtime.LockOSThread()
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0); e != 0 {
		fmt.Println("no_new_privs", e)
		return 2
	}
	if err := installFilter(); err != nil {
		fmt.Println(err)
		return 2
	}
	tab := syscalls[runtime.GOARCH]
	errno := func(nr uint32, a0, a1, a2 uintptr) syscall.Errno {
		r, _, e := syscall.RawSyscall(uintptr(nr), a0, a1, a2)
		if e == 0 && nr == tab.nr["socket"] {
			syscall.Close(int(r))
		}
		return e
	}
	dir, _ := os.MkdirTemp(os.Getenv("DC_TEST_DIR"), "fifo")
	fifo, _ := syscall.BytePtrFromString(dir + "/f")
	atFdcwd := -100
	res := map[string]syscall.Errno{
		"unshare":       errno(tab.nr["unshare"], 0x10000000, 0, 0),
		"keyctl":        errno(tab.nr["keyctl"], 0, 0, 0),
		"clone3":        errno(tab.nr["clone3"], 0, 0, 0),
		"netlink":       errno(tab.nr["socket"], 16, syscall.SOCK_DGRAM, 0),
		"inet":          errno(tab.nr["socket"], syscall.AF_INET, syscall.SOCK_STREAM, 0),
		"udp":           errno(tab.nr["socket"], syscall.AF_INET, syscall.SOCK_DGRAM, 0),
		"inet6_tcp":     errno(tab.nr["socket"], syscall.AF_INET6, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 6),
		"unix":          errno(tab.nr["socket"], syscall.AF_UNIX, syscall.SOCK_STREAM, 0),
		"mknodat_fifo":  errno(tab.nr["mknodat"], uintptr(atFdcwd), uintptr(unsafe.Pointer(fifo)), syscall.S_IFIFO|0o600),
		"personality":   errno(tab.nr["personality"], 0xffffffff, 0, 0),
		"getpid":        errno(syscall.SYS_GETPID, 0, 0, 0),
		"ptrace_attach": errno(tab.nr["ptrace"], 16, 1, 0),
	}
	os.RemoveAll(dir)
	// Every refused call of this architecture's table, with arguments
	// that would fail anyway (all bits set): refused means EPERM, a wrong
	// number would give the call's own error instead
	bad := ^uintptr(0)
	for _, name := range deniedCalls {
		if nr, ok := tab.nr[name]; ok {
			_, _, e := syscall.RawSyscall6(uintptr(nr), bad, bad, bad, bad, bad, bad)
			res["denied_"+name] = e
		}
	}
	var out []string
	for k, v := range res {
		out = append(out, fmt.Sprintf("%s=%d", k, int(v)))
	}
	fmt.Println(strings.Join(out, " "))
	// A program started from here is under the filter too
	cmd := exec.Command("/proc/self/exe")
	cmd.Env = []string{filterChild + "=status"}
	b, _ := cmd.Output()
	fmt.Print(string(b))
	return 0
}

// The filter on this machine's kernel: what it refuses and what it lets
// through, and that it is inherited.
func TestFilterOnKernel(t *testing.T) {
	if _, ok := syscalls[runtime.GOARCH]; !ok {
		t.Skip("no filter for " + runtime.GOARCH)
	}
	cmd := exec.Command("/proc/self/exe")
	cmd.Env = []string{filterChild + "=1", "DC_TEST_DIR=" + t.TempDir()}
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	got := map[string]string{}
	for _, f := range strings.Fields(string(b)) {
		if k, v, ok := strings.Cut(f, "="); ok {
			got[k] = v
		}
	}
	want := map[string]int{"unshare": int(syscall.EPERM), "keyctl": int(syscall.EPERM), "clone3": int(syscall.ENOSYS),
		"netlink": int(syscall.EAFNOSUPPORT), "inet": 0, "unix": 0, "mknodat_fifo": int(syscall.EPERM),
		"udp": int(syscall.EPERM), "inet6_tcp": 0,
		"personality": int(syscall.EPERM), "getpid": 0, "ptrace_attach": int(syscall.EPERM)}
	tab := syscalls[runtime.GOARCH]
	for _, name := range deniedCalls {
		if _, ok := tab.nr[name]; ok {
			want["denied_"+name] = int(syscall.EPERM)
		}
	}
	t.Logf("%s", strings.SplitN(string(b), "\n", 2)[0])
	for name, v := range want {
		if got[name] != fmt.Sprint(v) {
			t.Errorf("%s: %s, want %d", name, got[name], v)
		}
	}
	if !strings.Contains(string(b), "Seccomp:\t2") {
		t.Errorf("a program it starts is not filtered:\n%s", b)
	}
}

// The numbers of the tables against Go's own tables of each architecture
// (in the toolchain's source; skipped without it). Calls newer than Go's
// tables have the numbers every architecture shares since Linux 5.1.
func TestFilterNumbers(t *testing.T) {
	shared := map[string]uint32{"io_uring_setup": 425, "io_uring_enter": 426, "io_uring_register": 427, "open_tree": 428,
		"move_mount": 429, "fsopen": 430, "fsconfig": 431, "fsmount": 432, "fspick": 433, "clone3": 435, "mount_setattr": 442}
	for goarch, tab := range syscalls {
		b, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "src", "syscall", "zsysnum_linux_"+goarch+".go"))
		if err != nil {
			t.Skip("no toolchain source")
		}
		goNr := map[string]uint32{}
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) == 3 && f[1] == "=" && strings.HasPrefix(f[0], "SYS_") {
				if n, err := strconv.ParseUint(f[2], 10, 32); err == nil {
					goNr[strings.ToLower(strings.TrimPrefix(f[0], "SYS_"))] = uint32(n)
				}
			}
		}
		var unchecked []string
		for name, nr := range tab.nr {
			switch want, ok := goNr[name]; {
			case ok && want != nr:
				t.Errorf("%s %s: %d, Go has %d", goarch, name, nr, want)
			case ok:
			case shared[name] != 0:
				if shared[name] != nr {
					t.Errorf("%s %s: %d, all have %d", goarch, name, nr, shared[name])
				}
			default:
				unchecked = append(unchecked, fmt.Sprintf("%s=%d", name, nr))
			}
		}
		sort.Strings(unchecked)
		t.Logf("%s: not in Go's table: %v", goarch, unchecked)
	}
}
