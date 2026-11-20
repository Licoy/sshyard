package applock

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"net/url"
	"strconv"
	"time"
)

// Time-based one-time codes (RFC 6238) as authenticator apps make them by
// default: HMAC-SHA1, six digits, a new code every 30 seconds.
const (
	Digits     = 6
	period     = 30
	secretSize = 20
	// skew is how many steps a code may be early or late, for the clocks
	// of the phone and the computer that differ.
	skew = 1
)

// NewSecret is a random secret for a new authenticator.
func NewSecret() []byte {
	b := make([]byte, secretSize)
	rand.Read(b)
	return b
}

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// EncodeSecret is secret as it is typed into an authenticator app.
func EncodeSecret(secret []byte) string { return encoding.EncodeToString(secret) }

// URI is the otpauth URI a QR code gives an authenticator app.
func URI(secret []byte, issuer, account string) string {
	q := url.Values{}
	q.Set("secret", EncodeSecret(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", strconv.Itoa(Digits))
	q.Set("period", strconv.Itoa(period))
	u := url.URL{Scheme: "otpauth", Host: "totp", Path: "/" + issuer + ":" + account, RawQuery: q.Encode()}
	return u.String()
}

// Step is the time step of t.
func Step(t time.Time) int64 { return t.Unix() / period }

// Code is the code of secret at time step step.
func Code(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	h := hmac.New(sha1.New, secret)
	h.Write(msg[:])
	sum := h.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:]) & 0x7fffffff
	s := strconv.Itoa(int(n % 1_000_000))
	for len(s) < Digits {
		s = "0" + s
	}
	return s
}

// Verify reports whether code is secret's at t, within the skew, and at a
// step after last, which no code may be taken at again; step is the one
// it was taken at.
func Verify(secret []byte, code string, t time.Time, last int64) (step int64, ok bool) {
	if len(code) != Digits {
		return 0, false
	}
	now := Step(t)
	for s := now - skew; s <= now+skew; s++ {
		if s <= last {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(Code(secret, s)), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}
