package sig

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSignMatchesWorker(t *testing.T) {
	// Reference from: printf '1700000000.hello' | openssl dgst -sha256 -hmac sekret
	const want = "680baebe0df41ff87c859b56ab676d72f4e1a44c996813d60a07196ef726b472"
	if got := Sign("sekret", 1700000000, []byte("hello")); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestVerify(t *testing.T) {
	now := time.Unix(1700000000, 0)
	body := []byte("raw mime")
	h := http.Header{}
	SetHeaders(h, "s3cret", body, now)

	if err := Verify("s3cret", h, body, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := Verify("other", h, body, now); err == nil {
		t.Fatal("wrong secret accepted")
	}
	if err := Verify("s3cret", h, []byte("tampered"), now); err == nil {
		t.Fatal("tampered body accepted")
	}
	if err := Verify("s3cret", h, body, now.Add(6*time.Minute)); err == nil {
		t.Fatal("stale timestamp accepted")
	}
	if err := Verify("s3cret", http.Header{}, body, now); err == nil {
		t.Fatal("missing headers accepted")
	}
}

func TestPlausible(t *testing.T) {
	now := time.Unix(1700000000, 0)
	good := http.Header{}
	SetHeaders(good, "sekret", []byte("x"), now)
	if !Plausible(good, now) {
		t.Fatal("valid headers rejected")
	}
	for name, mutate := range map[string]func(http.Header){
		"no signature":  func(h http.Header) { h.Del(HeaderSignature) },
		"short":         func(h http.Header) { h.Set(HeaderSignature, "abcd") },
		"not hex":       func(h http.Header) { h.Set(HeaderSignature, strings.Repeat("zz", 32)) },
		"no timestamp":  func(h http.Header) { h.Del(HeaderTimestamp) },
		"bad timestamp": func(h http.Header) { h.Set(HeaderTimestamp, "soon") },
		"stale":         func(h http.Header) { h.Set(HeaderTimestamp, "1699990000") },
	} {
		h := good.Clone()
		mutate(h)
		if Plausible(h, now) {
			t.Errorf("%s: accepted", name)
		}
	}
}
