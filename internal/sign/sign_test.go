package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMakeFormat(t *testing.T) {
	t.Parallel()
	body := []byte(`{"id":"evt_1"}`)
	got := Make("s3cret", 1700000000, body)
	m := hmac.New(sha256.New, []byte("s3cret"))
	m.Write([]byte("1700000000." + string(body)))
	want := "t=1700000000,v1=" + hex.EncodeToString(m.Sum(nil))
	if got != want {
		t.Fatalf("Make = %q, want %q", got, want)
	}
	if Make("s3cret", 1700000000, body) != got {
		t.Fatal("Make is not deterministic")
	}
	if Make("other", 1700000000, body) == got || Make("s3cret", 1700000001, body) == got || Make("s3cret", 1700000000, []byte("x")) == got {
		t.Fatal("signature does not depend on all inputs")
	}
}

func TestVerify(t *testing.T) {
	t.Parallel()
	const secret = "whsec_test"
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"type":"draft.created"}`)
	good := Make(secret, now.Unix(), body)
	sig := strings.SplitN(good, ",v1=", 2)[1]
	ts := strconv.FormatInt(now.Unix(), 10)
	skew := 5 * time.Minute

	cases := []struct {
		name   string
		secret string
		header string
		body   []byte
		now    time.Time
		skew   time.Duration
		want   bool
	}{
		{"valid", secret, good, body, now, skew, true},
		{"reordered parts", secret, "v1=" + sig + ",t=" + ts, body, now, skew, true},
		{"spaces around parts", secret, " t=" + ts + " , v1=" + sig + " ", body, now, skew, true},
		{"extra unknown part", secret, good + ",v0=deadbeef", body, now, skew, true},
		{"uppercase hex sig", secret, "t=" + ts + ",v1=" + strings.ToUpper(sig), body, now, skew, true},
		{"within skew past", secret, good, body, now.Add(skew), skew, true},
		{"within skew future", secret, good, body, now.Add(-skew), skew, true},
		{"beyond skew past", secret, good, body, now.Add(skew + time.Second), skew, false},
		{"beyond skew future", secret, good, body, now.Add(-skew - time.Second), skew, false},
		{"zero skew exact", secret, good, body, now, 0, true},
		{"zero skew one second", secret, good, body, now.Add(time.Second), 0, false},
		{"wrong secret", "nope", good, body, now, skew, false},
		{"empty secret", "", good, body, now, skew, false},
		{"tampered body", secret, good, []byte(`{"type":"draft.committed"}`), now, skew, false},
		{"nil body", secret, good, nil, now, skew, false},
		{"empty header", secret, "", body, now, skew, false},
		{"missing t", secret, "v1=" + sig, body, now, skew, false},
		{"missing v1", secret, "t=" + ts, body, now, skew, false},
		{"non numeric t", secret, "t=abc,v1=" + sig, body, now, skew, false},
		{"non hex sig", secret, "t=" + ts + ",v1=zz" + sig[2:], body, now, skew, false},
		{"truncated sig", secret, "t=" + ts + ",v1=" + sig[:62], body, now, skew, false},
		{"odd length sig", secret, "t=" + ts + ",v1=" + sig[:63], body, now, skew, false},
		{"ts swapped keeps sig", secret, "t=" + strconv.FormatInt(now.Unix()+1, 10) + ",v1=" + sig, body, now, skew, false},
		{"garbage", secret, "hello world", body, now, skew, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Verify(c.secret, c.header, c.body, c.now, c.skew); got != c.want {
				t.Fatalf("Verify(%q) = %v, want %v", c.header, got, c.want)
			}
		})
	}
}

func TestVerifyEmptyBodyRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Now()
	h := Make("k", now.Unix(), nil)
	if !Verify("k", h, []byte{}, now, time.Second) {
		t.Fatal("nil and empty body should verify the same")
	}
}

func BenchmarkMake(b *testing.B) {
	body := []byte(strings.Repeat("x", 1024))
	b.ReportAllocs()
	for b.Loop() {
		_ = Make("whsec_bench", 1_700_000_000, body)
	}
}

func BenchmarkVerify(b *testing.B) {
	body := []byte(strings.Repeat("x", 1024))
	now := time.Unix(1_700_000_000, 0)
	h := Make("whsec_bench", now.Unix(), body)
	b.ReportAllocs()
	for b.Loop() {
		_ = Verify("whsec_bench", h, body, now, time.Minute)
	}
}
