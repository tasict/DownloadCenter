//go:build linux

package phpbox

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// The syscall filter of the plugin's process: a seccomp-BPF program built
// here from a table per architecture. It refuses what a program confined
// to the box has no use for and what would help it out of the box: new
// namespaces, mounts and chroot, tracing and other processes' memory, the
// kernel keyring, BPF, perf, userfaultfd, io_uring, kernel modules and
// kexec, handles to files by number, device nodes, swap, reboot, the
// clock, personality, the x86 LDT, and every socket but TCP over IPv4 and
// IPv6 and unix stream, datagram and seqpacket ones (other families,
// types and protocols can make the kernel load a protocol module; PHP
// reaches the relay over TCP and resolves nothing itself). A call from
// another architecture (32-bit code on a 64-bit kernel, the x32 ABI) ends
// the process. Everything else is allowed.

// Values of the kernel's seccomp and BPF interfaces (the same on every
// architecture).
const (
	prSetSeccomp      = 22
	seccompModeFilter = 2

	retKillProcess = 0x80000000
	retErrno       = 0x00050000
	retAllow       = 0x7fff0000

	bpfLdWAbs = 0x20 // BPF_LD | BPF_W | BPF_ABS
	bpfJeqK   = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
	bpfJgeK   = 0x35 // BPF_JMP | BPF_JGE | BPF_K
	bpfJsetK  = 0x45 // BPF_JMP | BPF_JSET | BPF_K
	bpfRetK   = 0x06 // BPF_RET | BPF_K
	bpfAndK   = 0x54 // BPF_ALU | BPF_AND | BPF_K

	// struct seccomp_data: nr, arch, instruction pointer, args (the low
	// half of the first argument: these are little-endian machines)
	offNr    = 0
	offArch  = 4
	offArg0  = 16
	offArg1  = 24
	offArg2  = 32
	x32Bit   = 0x40000000
	errEPERM = 1
	errNOSYS = 38
	errAFNO  = 97 // EAFNOSUPPORT

	afUnix  = 1
	afInet  = 2
	afInet6 = 10
	// socket types (without SOCK_NONBLOCK and SOCK_CLOEXEC) and protocols
	sockFlags     = 0x800 | 0x80000
	sockStream    = 1
	sockDgram     = 2
	sockSeqpacket = 5
	ipprotoTCP    = 6
	// CLONE_NEWNS, NEWCGROUP, NEWUTS, NEWIPC, NEWUSER, NEWPID, NEWNET
	cloneNamespaces = 0x00020000 | 0x02000000 | 0x04000000 | 0x08000000 | 0x10000000 | 0x20000000 | 0x40000000
)

type sockFilter struct {
	Code uint16
	Jt   uint8
	Jf   uint8
	K    uint32
}

type sockFprog struct {
	Len    uint16
	Filter *sockFilter
}

// sysTable is an architecture's audit value and syscall numbers.
type sysTable struct {
	arch uint32
	x32  bool // syscalls with the x32 bit set exist and are refused
	nr   map[string]uint32
}

// The calls refused with EPERM (clone3 gets ENOSYS, so that a C library
// falls back to clone, whose flags are checked; clone and socket are
// checked by their first argument).
var deniedCalls = []string{
	"unshare", "setns", "mount", "umount2", "pivot_root", "chroot", "modify_ldt",
	"open_tree", "move_mount", "fsopen", "fsconfig", "fsmount", "fspick", "mount_setattr",
	"ptrace", "process_vm_readv", "process_vm_writev",
	"keyctl", "add_key", "request_key",
	"bpf", "perf_event_open", "userfaultfd",
	"io_uring_setup", "io_uring_enter", "io_uring_register",
	"kexec_load", "kexec_file_load", "init_module", "finit_module", "delete_module",
	"open_by_handle_at", "name_to_handle_at", "mknod", "mknodat",
	"swapon", "swapoff", "reboot", "settimeofday", "clock_settime", "clock_settime64",
	"adjtimex", "clock_adjtime", "clock_adjtime64", "personality",
	"syslog", "acct", "quotactl", "lookup_dcookie", "vhangup", "uselib", "iopl", "ioperm",
}

// syscalls are the numbers of the calls the filter looks at; a call an
// architecture does not have is simply not in its table.
var syscalls = map[string]sysTable{
	"amd64": {arch: 0xc000003e, x32: true, nr: map[string]uint32{
		"socket": 41, "clone": 56, "clone3": 435,
		"unshare": 272, "setns": 308, "mount": 165, "umount2": 166, "pivot_root": 155, "chroot": 161,
		"open_tree": 428, "move_mount": 429, "fsopen": 430, "fsconfig": 431, "fsmount": 432, "fspick": 433, "mount_setattr": 442,
		"ptrace": 101, "process_vm_readv": 310, "process_vm_writev": 311,
		"keyctl": 250, "add_key": 248, "request_key": 249,
		"bpf": 321, "perf_event_open": 298, "userfaultfd": 323,
		"io_uring_setup": 425, "io_uring_enter": 426, "io_uring_register": 427,
		"kexec_load": 246, "kexec_file_load": 320, "init_module": 175, "finit_module": 313, "delete_module": 176,
		"open_by_handle_at": 304, "name_to_handle_at": 303, "mknod": 133, "mknodat": 259,
		"swapon": 167, "swapoff": 168, "reboot": 169, "settimeofday": 164, "clock_settime": 227,
		"adjtimex": 159, "clock_adjtime": 305, "personality": 135,
		"syslog": 103, "acct": 163, "quotactl": 179, "lookup_dcookie": 212, "vhangup": 153, "uselib": 134,
		"iopl": 172, "ioperm": 173, "modify_ldt": 154,
	}},
	"arm64": {arch: 0xc00000b7, nr: map[string]uint32{
		"socket": 198, "clone": 220, "clone3": 435,
		"unshare": 97, "setns": 268, "mount": 40, "umount2": 39, "pivot_root": 41, "chroot": 51,
		"open_tree": 428, "move_mount": 429, "fsopen": 430, "fsconfig": 431, "fsmount": 432, "fspick": 433, "mount_setattr": 442,
		"ptrace": 117, "process_vm_readv": 270, "process_vm_writev": 271,
		"keyctl": 219, "add_key": 217, "request_key": 218,
		"bpf": 280, "perf_event_open": 241, "userfaultfd": 282,
		"io_uring_setup": 425, "io_uring_enter": 426, "io_uring_register": 427,
		"kexec_load": 104, "kexec_file_load": 294, "init_module": 105, "finit_module": 273, "delete_module": 106,
		"open_by_handle_at": 265, "name_to_handle_at": 264, "mknodat": 33,
		"swapon": 224, "swapoff": 225, "reboot": 142, "settimeofday": 170, "clock_settime": 112,
		"adjtimex": 171, "clock_adjtime": 266, "personality": 92,
		"syslog": 116, "acct": 89, "quotactl": 60, "lookup_dcookie": 18, "vhangup": 58,
	}},
	"arm": {arch: 0x40000028, nr: map[string]uint32{
		"socket": 281, "clone": 120, "clone3": 435,
		"unshare": 337, "setns": 375, "mount": 21, "umount2": 52, "pivot_root": 218, "chroot": 61,
		"open_tree": 428, "move_mount": 429, "fsopen": 430, "fsconfig": 431, "fsmount": 432, "fspick": 433, "mount_setattr": 442,
		"ptrace": 26, "process_vm_readv": 376, "process_vm_writev": 377,
		"keyctl": 311, "add_key": 309, "request_key": 310,
		"bpf": 386, "perf_event_open": 364, "userfaultfd": 388,
		"io_uring_setup": 425, "io_uring_enter": 426, "io_uring_register": 427,
		"kexec_load": 347, "kexec_file_load": 401, "init_module": 128, "finit_module": 379, "delete_module": 129,
		"open_by_handle_at": 371, "name_to_handle_at": 370, "mknod": 14, "mknodat": 324,
		"swapon": 87, "swapoff": 115, "reboot": 88, "settimeofday": 79, "clock_settime": 262, "clock_settime64": 404,
		"adjtimex": 124, "clock_adjtime": 372, "clock_adjtime64": 405, "personality": 136,
		"syslog": 103, "acct": 51, "quotactl": 131, "lookup_dcookie": 249, "vhangup": 111, "uselib": 86,
	}},
}

// errNoFilter is an architecture without a filter: plugins do not run.
var errNoFilter = errors.New("no syscall filter for this architecture")

func stmt(code uint16, k uint32) sockFilter { return sockFilter{Code: code, K: k} }

// asm builds a part of a program whose conditional jumps name labels
// further on ("" is the next instruction).
type asm struct {
	p      []sockFilter
	labels map[string]int
	fixes  []fixup
}

type fixup struct {
	at   int
	t, f string
}

func (a *asm) op(in sockFilter)  { a.p = append(a.p, in) }
func (a *asm) label(name string) { a.labels[name] = len(a.p) }
func (a *asm) jeq(k uint32, t, f string) {
	a.fixes = append(a.fixes, fixup{len(a.p), t, f})
	a.op(sockFilter{Code: bpfJeqK, K: k})
}

// code resolves the labels.
func (a *asm) code() ([]sockFilter, error) {
	for _, x := range a.fixes {
		for i, l := range []string{x.t, x.f} {
			if l == "" {
				continue
			}
			to, ok := a.labels[l]
			if !ok || to <= x.at || to-x.at-1 > 255 {
				return nil, fmt.Errorf("bad jump to %s", l)
			}
			if i == 0 {
				a.p[x.at].Jt = uint8(to - x.at - 1)
			} else {
				a.p[x.at].Jf = uint8(to - x.at - 1)
			}
		}
	}
	return a.p, nil
}

// socketRule is the part of the program for socket(2), which the caller
// has matched: it ends in a return on every path.
func socketRule() ([]sockFilter, error) {
	a := &asm{labels: map[string]int{}}
	a.op(stmt(bpfLdWAbs, offArg0))
	a.jeq(afUnix, "unix", "")
	a.jeq(afInet, "inet", "")
	a.jeq(afInet6, "inet", "")
	a.op(stmt(bpfRetK, retErrno|errAFNO))
	a.label("unix")
	a.op(stmt(bpfLdWAbs, offArg1))
	a.op(stmt(bpfAndK, ^uint32(sockFlags)))
	a.jeq(sockStream, "proto0", "")
	a.jeq(sockDgram, "proto0", "")
	a.jeq(sockSeqpacket, "proto0", "deny")
	a.label("inet")
	a.op(stmt(bpfLdWAbs, offArg1))
	a.op(stmt(bpfAndK, ^uint32(sockFlags)))
	a.jeq(sockStream, "", "deny")
	a.op(stmt(bpfLdWAbs, offArg2))
	a.jeq(0, "allow", "")
	a.jeq(ipprotoTCP, "allow", "deny")
	a.label("proto0")
	a.op(stmt(bpfLdWAbs, offArg2))
	a.jeq(0, "allow", "deny")
	a.label("deny")
	a.op(stmt(bpfRetK, retErrno|errEPERM))
	a.label("allow")
	a.op(stmt(bpfRetK, retAllow))
	return a.code()
}

func jump(code uint16, k uint32, jt, jf uint8) sockFilter {
	return sockFilter{Code: code, Jt: jt, Jf: jf, K: k}
}

// buildFilter is the program for an architecture (runtime.GOARCH's name).
func buildFilter(goarch string) ([]sockFilter, error) {
	t, ok := syscalls[goarch]
	if !ok {
		return nil, errNoFilter
	}
	p := []sockFilter{
		stmt(bpfLdWAbs, offArch),
		jump(bpfJeqK, t.arch, 1, 0),
		stmt(bpfRetK, retKillProcess),
		stmt(bpfLdWAbs, offNr),
	}
	if t.x32 {
		p = append(p, jump(bpfJgeK, x32Bit, 0, 1), stmt(bpfRetK, retKillProcess))
	}
	// clone: no new namespaces
	p = append(p,
		jump(bpfJeqK, t.nr["clone"], 0, 4),
		stmt(bpfLdWAbs, offArg0),
		jump(bpfJsetK, cloneNamespaces, 0, 1),
		stmt(bpfRetK, retErrno|errEPERM),
		stmt(bpfRetK, retAllow),
	)
	// socket: TCP over IPv4 and IPv6, unix sockets
	sock, err := socketRule()
	if err != nil {
		return nil, err
	}
	if len(sock) > 255 {
		return nil, errors.New("socket rule too long")
	}
	p = append(p, jump(bpfJeqK, t.nr["socket"], 0, uint8(len(sock))))
	p = append(p, sock...)
	p = append(p, jump(bpfJeqK, t.nr["clone3"], 0, 1), stmt(bpfRetK, retErrno|errNOSYS))
	for _, name := range deniedCalls {
		if nr, ok := t.nr[name]; ok {
			p = append(p, jump(bpfJeqK, nr, 0, 1), stmt(bpfRetK, retErrno|errEPERM))
		}
	}
	return append(p, stmt(bpfRetK, retAllow)), nil
}

// installFilter puts the filter on the calling thread, which must be
// locked to its goroutine and have no_new_privs set; whatever the thread
// starts afterwards inherits it.
func installFilter() error {
	prog, err := buildFilter(runtime.GOARCH)
	if err != nil {
		return err
	}
	fp := sockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetSeccomp, seccompModeFilter, uintptr(unsafe.Pointer(&fp))); e != 0 {
		return fmt.Errorf("syscall filter: %w", e)
	}
	runtime.KeepAlive(prog)
	return nil
}
