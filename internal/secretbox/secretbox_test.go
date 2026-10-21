package secretbox

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const testKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func mustBox(t testing.TB, key string) *Box {
	t.Helper()
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNewKeyValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		key  string
		ok   bool
	}{
		{"valid", testKey, true},
		{"uppercase hex", strings.ToUpper(testKey), true},
		{"empty", "", false},
		{"short", testKey[:62], false},
		{"long", testKey + "00", false},
		{"odd length", testKey[:63], false},
		{"not hex", strings.Repeat("zz", 32), false},
		{"aes128 key", testKey[:32], false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			b, err := New(c.key)
			if (err == nil) != c.ok {
				t.Fatalf("New(%q) err = %v, want ok=%v", c.key, err, c.ok)
			}
			if c.ok && b == nil {
				t.Fatal("nil box")
			}
		})
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	t.Parallel()
	b := mustBox(t, testKey)
	for _, plain := range [][]byte{
		nil,
		{},
		[]byte("x"),
		[]byte(`{"token":"EAAG...","phone_id":"1234"}`),
		[]byte("Türkçe şifre: çğıöşü ₺"),
		bytes.Repeat([]byte{0xff}, 64<<10),
	} {
		ct, err := b.Seal(plain)
		if err != nil {
			t.Fatal(err)
		}
		if want := 1 + 12 + len(plain) + 16; len(ct) != want {
			t.Fatalf("len(ct) = %d, want %d", len(ct), want)
		}
		if ct[0] != 1 {
			t.Fatalf("version byte = %d", ct[0])
		}
		if len(plain) > 4 && bytes.Contains(ct, plain) {
			t.Fatal("ciphertext contains plaintext")
		}
		got, err := b.Open(ct)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("round trip mismatch: %q vs %q", got, plain)
		}
	}
}

func TestSealUsesFreshNonce(t *testing.T) {
	t.Parallel()
	b := mustBox(t, testKey)
	seen := map[string]bool{}
	for range 100 {
		ct, err := b.Seal([]byte("same"))
		if err != nil {
			t.Fatal(err)
		}
		k := string(ct[1:13])
		if seen[k] {
			t.Fatal("nonce reused")
		}
		seen[k] = true
	}
}

func TestOpenRejects(t *testing.T) {
	t.Parallel()
	b := mustBox(t, testKey)
	other := mustBox(t, strings.Repeat("ab", 32))
	ct, err := b.Seal([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	flip := func(i int) []byte {
		c := bytes.Clone(ct)
		c[i] ^= 0x01
		return c
	}
	cases := []struct {
		name string
		box  *Box
		ct   []byte
	}{
		{"nil", b, nil},
		{"empty", b, []byte{}},
		{"version only", b, []byte{1}},
		{"header only", b, ct[:13]},
		{"truncated tag", b, ct[:len(ct)-1]},
		{"wrong version", b, append([]byte{2}, ct[1:]...)},
		{"flipped nonce", b, flip(1)},
		{"flipped body", b, flip(13)},
		{"flipped tag", b, flip(len(ct) - 1)},
		{"wrong key", other, ct},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := c.box.Open(c.ct); !errors.Is(err, ErrCiphertext) {
				t.Fatalf("Open err = %v, want ErrCiphertext", err)
			}
		})
	}
}

func TestBoxConcurrentUse(t *testing.T) {
	t.Parallel()
	b := mustBox(t, testKey)
	done := make(chan error, 16)
	for i := range 16 {
		go func() {
			plain := bytes.Repeat([]byte{byte(i)}, 32)
			for range 50 {
				ct, err := b.Seal(plain)
				if err != nil {
					done <- err
					return
				}
				got, err := b.Open(ct)
				if err != nil || !bytes.Equal(got, plain) {
					done <- errors.New("round trip failed")
					return
				}
			}
			done <- nil
		}()
	}
	for range 16 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func BenchmarkSeal(b *testing.B) {
	box := mustBox(b, testKey)
	plain := bytes.Repeat([]byte("a"), 256)
	b.ReportAllocs()
	b.SetBytes(int64(len(plain)))
	for b.Loop() {
		_, _ = box.Seal(plain)
	}
}

func BenchmarkOpen(b *testing.B) {
	box := mustBox(b, testKey)
	ct, err := box.Seal(bytes.Repeat([]byte("a"), 256))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(256)
	for b.Loop() {
		_, _ = box.Open(ct)
	}
}
