package auth

import (
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := CheckPassword(h, "correct horse"); !ok || err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
	if ok, _ := CheckPassword(h, "wrong horse"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, err := CheckPassword("garbage", "x"); err == nil {
		t.Fatal("malformed hash accepted")
	}
	if h2, _ := HashPassword("correct horse"); h2 == h {
		t.Fatal("salt not random")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	for range 3 {
		if l.Blocked("ip:1", "user:a") {
			t.Fatal("blocked too early")
		}
		l.Fail("ip:1", "user:a")
	}
	if !l.Blocked("ip:2", "user:a") {
		t.Fatal("username limit not applied across IPs")
	}
	now = now.Add(61 * time.Second)
	if l.Blocked("ip:1", "user:a") {
		t.Fatal("window did not expire")
	}
	l.Fail("ip:1")
	l.Reset("ip:1")
	if len(l.fails) != 0 {
		t.Fatalf("not reset: %v", l.fails)
	}
}
