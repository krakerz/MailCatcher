// Package sig implements the HMAC scheme shared with the Cloudflare Worker:
// X-MC-Signature = hex(HMAC-SHA256(secret, "<unix ts>." + body)), X-MC-Timestamp = <unix ts>.
package sig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const (
	HeaderTimestamp = "X-MC-Timestamp"
	HeaderSignature = "X-MC-Signature"
	// Replay window, same as the Worker's.
	MaxSkew = 5 * time.Minute
)

var ErrInvalid = errors.New("invalid signature")

func Sign(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(ts, 10) + "."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// SetHeaders signs body and sets both headers on h.
func SetHeaders(h http.Header, secret string, body []byte, now time.Time) {
	ts := now.Unix()
	h.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(HeaderSignature, Sign(secret, ts, body))
}

func Verify(secret string, h http.Header, body []byte, now time.Time) error {
	ts, err := strconv.ParseInt(h.Get(HeaderTimestamp), 10, 64)
	if err != nil {
		return ErrInvalid
	}
	if d := now.Sub(time.Unix(ts, 0)); d > MaxSkew || d < -MaxSkew {
		return ErrInvalid
	}
	got, err := hex.DecodeString(h.Get(HeaderSignature))
	if err != nil {
		return ErrInvalid
	}
	want, _ := hex.DecodeString(Sign(secret, ts, body))
	if !hmac.Equal(got, want) {
		return ErrInvalid
	}
	return nil
}
