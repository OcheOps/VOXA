package main

import "testing"

func TestLoadMediaLimits(t *testing.T) {
	limits, err := loadMediaLimits(env(map[string]string{}))
	if err != nil || limits.MaxRecordingBytes != 1<<30 || limits.MaxChunkBytes != 8<<20 {
		t.Fatalf("defaults = %#v err=%v", limits, err)
	}
	limits, err = loadMediaLimits(env(map[string]string{mediaMaxRecordingEnv: "256"}))
	if err != nil || limits.MaxRecordingBytes != 256<<20 {
		t.Fatalf("configured = %#v err=%v", limits, err)
	}
	for _, bad := range []string{"0", "-1", "lots", "2000000"} {
		if _, err := loadMediaLimits(env(map[string]string{mediaMaxRecordingEnv: bad})); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
