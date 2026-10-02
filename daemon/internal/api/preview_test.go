package api

import "testing"

func TestContiguous(t *testing.T) {
	bits := parseBits("e0", 8) // 1110 0000
	if got := contiguous(bits, 100, 0, 250); got != 250 {
		t.Errorf("file within the first 3 pieces = %d", got)
	}
	if got := contiguous(bits, 100, 0, 1000); got != 300 {
		t.Errorf("prefix = %d, want 300", got)
	}
	if got := contiguous(bits, 100, 150, 500); got != 150 {
		t.Errorf("file at offset 150 = %d, want 150", got)
	}
	if got := contiguous(bits, 100, 350, 100); got != 0 {
		t.Errorf("missing piece = %d, want 0", got)
	}
}

func TestFileType(t *testing.T) {
	if k, p := fileType("a/b.MP4"); k != "video" || !p {
		t.Error("mp4")
	}
	if k, p := fileType("x.mkv"); k != "video" || p {
		t.Error("mkv must not be playable")
	}
	if k, _ := fileType("x.zip"); k != "archive" {
		t.Error("zip")
	}
	if k, p := fileType("a/movie.mp4.dsdownload"); k != "video" || !p {
		t.Error("official unfinished mp4 must stay a playable video")
	}
}
