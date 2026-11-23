package core

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
)

// Memory profiles: on NAS models with little memory the engines use
// settings that need less of it, and some defaults are lower.
const (
	ProfileNormal = "normal"
	ProfileLow    = "low"
)

// lowMemoryMiB is the most memory (MemTotal) a NAS of the low profile has:
// models sold with 512 MB or 1 GB, not those with 2 GB.
const lowMemoryMiB = 1536

// lowMemoryLimit is dcd's soft memory limit with the low profile.
const lowMemoryLimit = 64 << 20

var memoryProfile atomic.Value // string

// MemoryProfile is the profile dcd runs with (normal until set).
func MemoryProfile() string {
	if p, _ := memoryProfile.Load().(string); p != "" {
		return p
	}
	return ProfileNormal
}

// SetMemoryProfile sets the profile; dcd does it once, before the settings
// are loaded, because the defaults depend on it.
func SetMemoryProfile(p string) {
	if p != ProfileLow {
		p = ProfileNormal
	}
	memoryProfile.Store(p)
}

// DetectMemoryProfile decides the profile from MemTotal in meminfo
// (/proc/meminfo), unless the first line of data/memory_profile names one.
// It returns the profile, MemTotal in MiB (0 when unknown) and whether the
// file chose it.
func DetectMemoryProfile(meminfo, dataDir string) (profile string, totalMiB int64, forced bool) {
	totalMiB = memTotalMiB(meminfo)
	if b, err := os.ReadFile(filepath.Join(dataDir, "memory_profile")); err == nil {
		line, _, _ := strings.Cut(string(b), "\n")
		switch p := strings.ToLower(strings.TrimSpace(line)); p {
		case ProfileLow, ProfileNormal:
			return p, totalMiB, true
		}
	}
	if totalMiB > 0 && totalMiB <= lowMemoryMiB {
		return ProfileLow, totalMiB, false
	}
	return ProfileNormal, totalMiB, false
}

func memTotalMiB(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			var kb int64
			for _, c := range fields[1] {
				if c < '0' || c > '9' {
					return 0
				}
				kb = kb*10 + int64(c-'0')
			}
			return kb / 1024
		}
	}
	return 0
}

// ApplyMemoryLimit sets dcd's soft memory limit for the profile: 64 MiB with
// the low profile, none otherwise. GOMEMLIMIT, which the Go runtime reads
// itself, wins. It returns the limit set (0 when none was set here).
func ApplyMemoryLimit(profile string, getenv func(string) string) int64 {
	if profile != ProfileLow || getenv("GOMEMLIMIT") != "" {
		return 0
	}
	debug.SetMemoryLimit(lowMemoryLimit)
	return lowMemoryLimit
}
