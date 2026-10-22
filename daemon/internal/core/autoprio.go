package core

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"downloadcenter/internal/engine"
	"downloadcenter/internal/torrent"
)

// videoExt are the videos as the add dialog's "Only videos" sees them.
var videoExt = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".m4v": true,
	".webm": true, ".ts": true, ".wmv": true, ".flv": true,
}

// prioFile is one file of a torrent as the automatic priority sees it.
type prioFile struct {
	Index  int
	Path   string
	Size   int64
	Wanted bool // to be downloaded
	Pad    bool // BEP 47 padding file
}

// mainVideo is the file a torrent with several files to download is most
// likely added for: the largest video among them, the lowest index on a
// tie. -1 when fewer than two files are to be downloaded or none is a video.
func mainVideo(files []prioFile) int {
	n, best := 0, -1
	var size int64
	for _, f := range files {
		if !f.Wanted || f.Pad {
			continue
		}
		n++
		if !videoExt[strings.ToLower(path.Ext(f.Path))] {
			continue
		}
		if best < 0 || f.Size > size || f.Size == size && f.Index < best {
			best, size = f.Index, f.Size
		}
	}
	if n < 2 {
		return -1
	}
	return best
}

// autoPriorities gives the main video high priority (7) and the other files
// to download medium (4), libtorrent's default, so the tab shows what the
// engine does. nil when the torrent has no main video.
func autoPriorities(files []prioFile) (map[string]int, prioFile) {
	main := mainVideo(files)
	if main < 0 {
		return nil, prioFile{}
	}
	pr := map[string]int{}
	var mf prioFile
	for _, f := range files {
		if f.Wanted && !f.Pad {
			pr[strconv.Itoa(f.Index)] = 4
		}
		if f.Index == main {
			mf = f
		}
	}
	pr[strconv.Itoa(main)] = 7
	return pr, mf
}

func metaPrioFiles(meta *torrent.Meta, sel []int) []prioFile {
	out := make([]prioFile, 0, len(meta.Files))
	for _, f := range meta.Files {
		out = append(out, prioFile{Index: f.Index, Path: f.Path, Size: f.Size, Pad: f.Pad,
			Wanted: sel == nil || containsInt(sel, f.Index)})
	}
	return out
}

func enginePrioFiles(files []engine.File) []prioFile {
	out := make([]prioFile, 0, len(files))
	for _, f := range files {
		out = append(out, prioFile{Index: f.Index, Path: f.Path, Size: f.Size, Pad: f.Pad, Wanted: f.Selected})
	}
	return out
}

// intKeys turns saved priorities (keyed by the file index as text) into the
// engines' form; nil when none are saved.
func intKeys(pr map[string]int) map[int]int {
	if len(pr) == 0 {
		return nil
	}
	out := make(map[int]int, len(pr))
	for k, v := range pr {
		if i, err := strconv.Atoi(k); err == nil {
			out[i] = v
		}
	}
	return out
}

func (m *Manager) logMainVideo(hash string, f prioFile) {
	m.Log(hash, fmt.Sprintf("Set the largest video to high priority: %s", f.Path))
}

// autoPrioritize decides the priorities of a magnet task whose file list has
// just arrived (AutoPrio), once, and hands them to the engine. Caller holds
// m.mu.
func (m *Manager) autoPrioritize(t *Task, files []engine.File, e engine.Engine) {
	t.Options.AutoPrio = false
	m.markDirty(t)
	pr, mf := autoPriorities(enginePrioFiles(files))
	if pr == nil {
		return
	}
	t.Options.Priorities = pr
	var sel []int
	for _, f := range files {
		if f.Selected {
			sel = append(sel, f.Index)
		}
	}
	if t.EngineRef != "" {
		e.SetFiles(t.EngineRef, sel, intKeys(pr))
	}
	m.logMainVideo(t.Hash, mf)
	m.syncFiles(t, files)
}
