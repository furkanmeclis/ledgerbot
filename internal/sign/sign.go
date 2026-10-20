// Package sign implements ledgerbot's HMAC signature scheme, shared by
// outbound webhooks, plugin calls and signed REST requests:
//
//	X-Ledgerbot-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>
package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Header is the signature header name.
const Header = "X-Ledgerbot-Signature"

// Make returns the header value for body at time t.
func Make(secret string, t int64, body []byte) string {
	ts := strconv.FormatInt(t, 10)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac(secret, ts, body))
}

// Verify checks a header value within the allowed clock skew.
func Verify(secret, header string, body []byte, now time.Time, skew time.Duration) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || now.Sub(time.Unix(t, 0)).Abs() > skew {
		return false
	}
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	return hmac.Equal(want, mac(secret, ts, body))
}

func mac(secret, ts string, body []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts))
	m.Write([]byte{'.'})
	m.Write(body)
	return m.Sum(nil)
}
