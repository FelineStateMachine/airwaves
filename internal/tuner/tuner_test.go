package tuner

import "testing"

func TestDemoCommandIsStablePerChannel(t *testing.T) {
	a := DemoCommand("ffmpeg", "7.1", "KMGH")
	if b := DemoCommand("ffmpeg", "7.1", "KMGH"); a != b {
		t.Error("demo pattern changed between runs of the same channel")
	}
	if c := DemoCommand("ffmpeg", "9.1", "KUSA"); a == c {
		t.Error("two channels share a demo command")
	}
}
