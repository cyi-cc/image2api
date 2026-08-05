package leonardo

import "testing"

func TestLeonardoVideoProfile(t *testing.T) {
	tests := map[string]string{
		"leonardo-seedance-fast":   "seedance-2.0-fast",
		"leonardo-seedance-2.0":    "seedance-2.0",
		"leonardo-seedance-mini":   "seedance-2.0-mini",
		"leonardo-minimax-h3":      "minimax-h3",
		"leonardo-happy-horse-1.1": "happy-horse-1.1",
	}
	for input, want := range tests {
		profile, ok := leonardoVideoProfile(input)
		if !ok || profile.UpstreamID != want {
			t.Fatalf("leonardoVideoProfile(%q) = %#v/%v, want upstream %q", input, profile, ok, want)
		}
	}
	if _, ok := leonardoVideoProfile("unknown"); ok {
		t.Fatal("leonardoVideoProfile accepted unknown model")
	}
}

func TestVideoDimensions(t *testing.T) {
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
		{ratio: "4:3", resolution: "720p", wantW: 1108, wantH: 832},
		{ratio: "3:4", resolution: "1080p", wantW: 1248, wantH: 1662},
		{ratio: "1:1", resolution: "1080p", wantW: 1440, wantH: 1440},
		{ratio: "16:9", resolution: "2K", wantW: 2560, wantH: 1440},
		{ratio: "21:9", resolution: "2K", wantW: 2560, wantH: 1080},
	}
	for _, tt := range tests {
		gotW, gotH := videoDimensions(tt.ratio, tt.resolution)
		if gotW != tt.wantW || gotH != tt.wantH {
			t.Fatalf("videoDimensions(%q, %q) = %dx%d, want %dx%d", tt.ratio, tt.resolution, gotW, gotH, tt.wantW, tt.wantH)
		}
	}
}

func TestVideoFrameGuidances(t *testing.T) {
	got := videoGuidances([]string{"start", "end", "ignored"}, "frame", videoModelProfile{MaxReferences: 4, SupportsEndFrame: true})
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

func TestHappyHorseGuidancesRespectCapabilities(t *testing.T) {
	profile, ok := leonardoVideoProfile("leonardo-happy-horse-1.1")
	if !ok {
		t.Fatal("happy horse profile missing")
	}
	frame := videoGuidances([]string{"start", "must-not-be-end"}, "frame", profile)
	if _, ok := frame["end_frame"]; ok {
		t.Fatal("happy horse must not emit unsupported end_frame")
	}
	ids := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}
	refs := videoGuidances(ids, "asset", profile)["image_reference"].([]any)
	if len(refs) != 9 {
		t.Fatalf("happy horse image references = %d, want 9", len(refs))
	}
}
