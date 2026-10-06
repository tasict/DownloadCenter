package api

import (
	"archive/tar"
	"encoding/binary"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"downloadcenter/internal/auth"
	"downloadcenter/internal/core"
)

// previewFile is a file of a task with its readable prefix.
type previewFile struct {
	Index      int    `json:"index"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Contiguous int64  `json:"contiguous"` // bytes readable from the start
	Downloaded int64  `json:"downloaded"`
	Type       string `json:"type"` // video | audio | image | text | archive | other
	Playable   bool   `json:"playable"`
	real       string
	offset     int64
}

var inlineTypes = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".webm": "video/webm", ".ogv": "video/ogg", ".mov": "video/quicktime",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".aac": "audio/aac", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg", ".flac": "audio/flac", ".wav": "audio/wav",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif",
	".txt": "text/plain; charset=utf-8", ".nfo": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8", ".srt": "text/plain; charset=utf-8",
	".vtt": "text/plain; charset=utf-8", ".log": "text/plain; charset=utf-8", ".sfv": "text/plain; charset=utf-8", ".json": "text/plain; charset=utf-8", ".csv": "text/plain; charset=utf-8",
}

// cleanName drops the suffix the official Download Station gives unfinished
// files (imported tasks keep their official temp files), so the type of
// "movie.mp4.dsdownload" is that of "movie.mp4".
func cleanName(name string) string {
	if strings.HasSuffix(strings.ToLower(name), ".dsdownload") {
		return name[:len(name)-len(".dsdownload")]
	}
	return name
}

func fileType(name string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(cleanName(name)))
	ct := inlineTypes[ext]
	switch {
	case strings.HasPrefix(ct, "video/"):
		return "video", ext != ".mov" && ext != ".ogv"
	case strings.HasPrefix(ct, "audio/"):
		return "audio", true
	case strings.HasPrefix(ct, "image/"):
		return "image", true
	case strings.HasPrefix(ct, "text/"):
		return "text", true
	}
	switch ext {
	case ".zip", ".tar", ".cbz":
		return "archive", true
	case ".mkv", ".avi", ".wmv", ".flv", ".ts", ".m2ts":
		return "video", false
	case ".rar", ".7z", ".gz", ".xz", ".bz2", ".iso", ".img", ".dmg":
		return "archive", false
	}
	return "other", false
}

// previewFiles computes, for every file, how much is readable from its start.
func (s *Server) previewFiles(t *core.Task) []previewFile {
	rows := s.M.Files(t.Hash)
	var out []previewFile
	done := t.State == core.StDone || t.State == core.StSeeding
	base := t.SaveDir()
	if t.Kind != core.KindBT {
		base = t.WorkDir
		if t.State == core.StDone && t.DataPath != "" {
			base = filepath.Dir(t.DataPath)
		}
	}
	bits := parseBits(t.Bitfield, t.NumPieces)
	var offset int64
	if len(rows) == 0 && t.Kind != core.KindBT {
		name := t.Name
		if t.DataPath != "" {
			name = filepath.Base(t.DataPath)
		}
		rows = []core.FileRow{{Index: 0, Path: name, Size: t.Size, Done: t.DoneBytes, Priority: 1}}
	}
	for _, f := range rows {
		pf := previewFile{Index: f.Index, Path: f.Path, Size: f.Size, Downloaded: f.Done, offset: offset}
		pf.Type, pf.Playable = fileType(f.Path)
		pf.real = filepath.Join(base, f.Path)
		if t.Kind != core.KindBT {
			if t.State == core.StDone && t.DataPath != "" {
				pf.real = t.DataPath
			} else {
				pf.real = filepath.Join(t.WorkDir, filepath.Base(f.Path))
			}
		}
		switch {
		case done || f.Done >= f.Size:
			pf.Contiguous = f.Size
			pf.Downloaded = f.Size
		case t.PieceLen > 0 && len(bits) > 0:
			pf.Contiguous = contiguous(bits, t.PieceLen, offset, f.Size)
		}
		out = append(out, pf)
		offset += f.Size
	}
	return out
}

func parseBits(hexs string, n int) []bool {
	if hexs == "" {
		return nil
	}
	out := make([]bool, 0, len(hexs)*4)
	for _, c := range hexs {
		v, err := strconv.ParseUint(string(c), 16, 8)
		if err != nil {
			return nil
		}
		for i := 3; i >= 0; i-- {
			out = append(out, v&(1<<uint(i)) != 0)
		}
	}
	if n > 0 && n < len(out) {
		out = out[:n]
	}
	return out
}

func contiguous(bits []bool, plen, off, size int64) int64 {
	if size <= 0 {
		return 0
	}
	p := off / plen
	end := off
	for int(p) < len(bits) && bits[p] {
		end = (p + 1) * plen
		if end >= off+size {
			return size
		}
		p++
	}
	if end <= off {
		return 0
	}
	return end - off
}

func (s *Server) previewRoutes() {
	s.Route("GET /tasks/{id}/preview-info", "tasks:read", 0, func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
		t := s.visibleTask(w, p, r.PathValue("id"))
		if t == nil {
			return
		}
		files := s.previewFiles(t)
		if files == nil {
			files = []previewFile{}
		}
		seq := false
		if bt := s.M.BTEngine(); bt != nil && t.Kind == core.KindBT {
			seq = bt.Caps().Sequential
		}
		OK(w, map[string]any{"files": files, "sequential_supported": seq, "sequential": t.Options.Sequential})
	})
	s.Route("GET /tasks/{id}/preview", "tasks:read", 0, s.preview)
	s.Route("GET /tasks/{id}/archive", "tasks:read", 0, s.archiveList)
}

func (s *Server) previewTarget(w http.ResponseWriter, r *http.Request, p *auth.Principal) (*core.Task, *previewFile) {
	t := s.visibleTask(w, p, r.PathValue("id"))
	if t == nil {
		return nil, nil
	}
	idx, err := strconv.Atoi(r.URL.Query().Get("file"))
	if err != nil {
		idx = 0
	}
	for _, f := range s.previewFiles(t) {
		if f.Index != idx {
			continue
		}
		// Only files inside the task's own folder
		real, err := filepath.EvalSymlinks(f.real)
		if err != nil {
			Error(w, 404, "not_found", "The file does not exist yet")
			return nil, nil
		}
		allowed := []string{t.TempDir, t.WorkDir, t.DataPath}
		if t.DataPath != "" {
			allowed = append(allowed, filepath.Dir(t.DataPath))
		}
		okInside := false
		for _, a := range allowed {
			if a == "" {
				continue
			}
			ar, err := filepath.EvalSymlinks(a)
			if err != nil {
				continue
			}
			if real == ar || strings.HasPrefix(real, ar+"/") {
				okInside = true
			}
		}
		if t.Kind == core.KindBT && t.State == core.StDone && t.DataPath != "" {
			dr, _ := filepath.EvalSymlinks(t.DataPath)
			okInside = real == dr || strings.HasPrefix(real, dr+"/")
		}
		if !okInside {
			Error(w, 403, "forbidden", "Only files in this task's folder can be previewed")
			return nil, nil
		}
		f.real = real
		return t, &f
	}
	Error(w, 404, "not_found", "File not found")
	return nil, nil
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	_, f := s.previewTarget(w, r, p)
	if f == nil {
		return
	}
	if f.Contiguous <= 0 {
		Error(w, 409, "not_ready", "The beginning of this file has not been downloaded yet")
		return
	}
	fp, err := os.Open(f.real)
	if err != nil {
		Error(w, 404, "not_found", "The file does not exist yet")
		return
	}
	defer fp.Close()
	st, _ := fp.Stat()
	n := f.Contiguous
	if st != nil && st.Size() < n {
		n = st.Size()
	}
	name := cleanName(filepath.Base(f.Path))
	ct, inline := inlineTypes[strings.ToLower(filepath.Ext(name))]
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "no-store")
	if inline && r.URL.Query().Get("download") != "1" {
		h.Set("Content-Type", ct)
		h.Set("Content-Disposition", contentDisposition("inline", name))
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", contentDisposition("attachment", name))
	}
	h.Set("X-DC-Contiguous", strconv.FormatInt(n, 10))
	h.Set("X-DC-Size", strconv.FormatInt(f.Size, 10))
	http.ServeContent(w, r, "", time.Time{}, io.NewSectionReader(fp, 0, n))
}

func contentDisposition(kind, name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return kind + `; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(name)
}

// archiveList lists the entries of a zip or tar that are readable within
// the downloaded prefix (local headers are sequential, so a partial archive
// can still be listed up to the first missing piece).
func (s *Server) archiveList(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
	_, f := s.previewTarget(w, r, p)
	if f == nil {
		return
	}
	fp, err := os.Open(f.real)
	if err != nil {
		Error(w, 404, "not_found", "The file does not exist yet")
		return
	}
	defer fp.Close()
	limit := f.Contiguous
	sr := io.NewSectionReader(fp, 0, limit)
	type entry struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		Dir  bool   `json:"dir"`
	}
	var out []entry
	complete := limit >= f.Size
	ext := strings.ToLower(filepath.Ext(cleanName(f.Path)))
	switch ext {
	case ".zip", ".cbz":
		var off int64
		hdr := make([]byte, 30)
		for len(out) < 5000 {
			if _, err := sr.ReadAt(hdr, off); err != nil {
				break
			}
			if binary.LittleEndian.Uint32(hdr) != 0x04034b50 {
				break
			}
			flags := binary.LittleEndian.Uint16(hdr[6:])
			csize := int64(binary.LittleEndian.Uint32(hdr[18:]))
			usize := int64(binary.LittleEndian.Uint32(hdr[22:]))
			nlen := int64(binary.LittleEndian.Uint16(hdr[26:]))
			xlen := int64(binary.LittleEndian.Uint16(hdr[28:]))
			name := make([]byte, nlen)
			if _, err := sr.ReadAt(name, off+30); err != nil {
				break
			}
			out = append(out, entry{Name: string(name), Size: usize, Dir: strings.HasSuffix(string(name), "/")})
			if flags&0x08 != 0 && csize == 0 {
				// Sizes follow the data (streamed zip): cannot skip safely
				break
			}
			off += 30 + nlen + xlen + csize
		}
	case ".tar":
		tr := tar.NewReader(sr)
		for len(out) < 5000 {
			h, err := tr.Next()
			if err != nil {
				break
			}
			out = append(out, entry{Name: h.Name, Size: h.Size, Dir: h.Typeflag == tar.TypeDir})
		}
	default:
		Error(w, 400, "unsupported", "This type of archive cannot be previewed while downloading")
		return
	}
	if out == nil {
		out = []entry{}
	}
	OK(w, map[string]any{"entries": out, "complete": complete, "readable": limit, "type": mime.TypeByExtension(ext)})
}
