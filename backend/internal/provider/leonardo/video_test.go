package leonardo

import "testing"

func TestNormalizeSeedanceModel(t *testing.T) {
	tests := map[string]string{
		"leonardo-seedance-fast": "seedance-2.0-fast",
		"leonardo-seedance-2.0":  "seedance-2.0",
		"leonardo-seedance-mini": "seedance-2.0-mini",
	}
	for input, want := range tests {
		if got := normalizeSeedanceModel(input); got != want {
			t.Fatalf("normalizeSeedanceModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSeedanceDimensions(t *testing.T) {
	tests := []struct {
		ratio      string
		resolution string
		wantW      int
		wantH      int
	}{
		{ratio: "16:9", resolution: "720p", wantW: 1280, wantH: 720},
		{ratio: "9:16", resolution: "720p", wantW: 720, wantH: 1280},
		{ratio: "16:9", resolution: "1080p", wantW: 1920, wantH: 1080},
		{ratio: "9:16", resolution: "1080p", wantW: 1080, wantH: 1920},
	}
	for _, tt := range tests {
		gotW, gotH := seedanceDimensions(tt.ratio, tt.resolution)
		if gotW != tt.wantW || gotH != tt.wantH {
			t.Fatalf("seedanceDimensions(%q, %q) = %dx%d, want %dx%d", tt.ratio, tt.resolution, gotW, gotH, tt.wantW, tt.wantH)
		}
	}
}

func TestSeedanceResolutionMode(t *testing.T) {
	tests := map[string]string{
		"720p":  "RESOLUTION_720",
		"1080p": "RESOLUTION_1080",
		"480p":  "RESOLUTION_480",
	}
	for input, want := range tests {
		if got := seedanceResolutionMode(input); got != want {
			t.Fatalf("seedanceResolutionMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSeedanceFrameGuidances(t *testing.T) {
	got := seedanceGuidances([]string{"start", "end", "ignored"}, "frame")
	if _, ok := got["start_frame"]; !ok {
		t.Fatal("start_frame missing")
	}
	if _, ok := got["end_frame"]; !ok {
		t.Fatal("end_frame missing")
	}
	if _, ok := got["image_reference"]; ok {
		t.Fatal("frame mode must not emit image_reference")
	}
}
