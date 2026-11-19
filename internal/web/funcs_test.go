package web

import (
	"testing"
	"time"
)

func TestZoneLabel(t *testing.T) {
	saved := time.Local
	defer func() { time.Local = saved }()
	for _, tc := range []struct{ zone, want string }{
		{"Asia/Singapore", "Singapore time (UTC+08:00)"},
		{"America/Argentina/Buenos_Aires", "Buenos Aires time (UTC-03:00)"},
		{"UTC", "UTC"},
	} {
		loc, err := time.LoadLocation(tc.zone)
		if err != nil {
			t.Fatal(err)
		}
		time.Local = loc
		if got := zoneLabel(); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.zone, got, tc.want)
		}
	}
}
