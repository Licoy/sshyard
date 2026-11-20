package applock

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Licoy/sshyard/internal/vault"
)

// cheap derives fast, for tests.
var cheap = vault.Params{Time: 1, Memory: 8, Threads: 1}

// TestCodeRFC6238: the SHA1 test vectors of RFC 6238, appendix B, to six
// digits.
func TestCodeRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, v := range []struct {
		unix int64
		code string
	}{
		{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"},
		{1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"},
	} {
		if got := Code(secret, Step(time.Unix(v.unix, 0))); got != v.code {
			t.Errorf("at %d: %s, want %s", v.unix, got, v.code)
		}
	}
}

// TestVerify: a code is taken a step early or late, never further off,
// never at or before the last step taken, and never in another length.
func TestVerify(t *testing.T) {
	secret := NewSecret()
	now := time.Unix(1_700_000_000, 0)
	step := Step(now)
	for d := int64(-1); d <= 1; d++ {
		if got, ok := Verify(secret, Code(secret, step+d), now, 0); !ok || got != step+d {
			t.Errorf("skew %d: %d %v", d, got, ok)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := Verify(secret, Code(secret, step+d), now, 0); ok {
			t.Errorf("skew %d taken", d)
		}
	}
	if _, ok := Verify(secret, Code(secret, step), now, step); ok {
		t.Error("a code taken again")
	}
	if _, ok := Verify(secret, Code(secret, step-1), now, step-1); ok {
		t.Error("an earlier code taken after a later one")
	}
	if _, ok := Verify(secret, Code(secret, step+1), now, step); !ok {
		t.Error("the next code refused")
	}
	if _, ok := Verify(secret, Code(secret, step)+"0", now, 0); ok {
		t.Error("seven digits taken")
	}
	if _, ok := Verify(NewSecret(), Code(secret, step), now, 0); ok {
		t.Error("another secret's code taken")
	}
}

// TestPasswordVerifier: the verifier opens with its password only, and
// holds nothing of it.
func TestPasswordVerifier(t *testing.T) {
	v, err := NewVerifier([]byte("correct horse"), cheap)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := CheckPassword(v, []byte("correct horse")); !ok || err != nil {
		t.Fatalf("right password: %v %v", ok, err)
	}
	if ok, err := CheckPassword(v, []byte("wrong horse")); ok || err != nil {
		t.Fatalf("wrong password: %v %v", ok, err)
	}
	if ok, err := CheckPassword([]byte("{}"), []byte("correct horse")); ok || err == nil {
		t.Fatalf("broken verifier: %v %v", ok, err)
	}
}

// TestThrottle: the first tries are free, then each failure doubles the
// wait up to its cap, which a clock set back cannot exceed; a success
// starts over.
func TestThrottle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	var c Config
	for range freeTries {
		c.Fail(now)
	}
	if w := c.Wait(now); w != 0 {
		t.Fatalf("free tries wait %v", w)
	}
	c.Fail(now)
	if w := c.Wait(now); w != firstWait {
		t.Fatalf("first wait %v", w)
	}
	c.Fail(now)
	if w := c.Wait(now); w != 2*firstWait {
		t.Fatalf("second wait %v", w)
	}
	if w := c.Wait(now.Add(2 * firstWait)); w != 0 {
		t.Fatalf("wait after it passed %v", w)
	}
	for range 20 {
		c.Fail(now)
	}
	if w := c.Wait(now); w != maxWait {
		t.Fatalf("capped wait %v", w)
	}
	if w := c.Wait(now.Add(-24 * time.Hour)); w != maxWait {
		t.Fatalf("clock set back: wait %v", w)
	}
	c.Succeed()
	if c.Failures != 0 || c.Wait(now) != 0 {
		t.Fatalf("after success: %+v", c)
	}
}

// TestLoadSave: a missing file is the default, unlocked; a saved lock
// comes back as saved; a damaged or unknown one is an error, never the
// default, so that the app stays locked.
func TestLoadSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, File)
	c, err := Load(path)
	if err != nil || c.Enabled() || c.IdleMinutes != 15 || !c.LockOnSleep {
		t.Fatalf("missing: %+v %v", c, err)
	}
	v, err := NewVerifier([]byte("correct horse"), cheap)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Method: Both, Verifier: v, IdleMinutes: 5, LastStep: 42, Failures: 6, RetryAt: time.Unix(1_700_000_000, 0).UTC()}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.Method != Both || got.IdleMinutes != 5 || got.LockOnSleep || got.LastStep != 42 || got.Failures != 6 || !got.RetryAt.Equal(want.RetryAt) {
		t.Fatalf("loaded %+v %v", got, err)
	}
	if ok, _ := CheckPassword(got.Verifier, []byte("correct horse")); !ok {
		t.Fatal("verifier lost")
	}
	for name, body := range map[string]string{
		"damaged":     "{",
		"unknown":     `{"method":"face"}`,
		"no verifier": `{"method":"password"}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if c, err := Load(path); err == nil {
			t.Errorf("%s: loaded %+v", name, c)
		}
	}
}

// TestAdopt: the store's record decides how the lock opens, whatever the
// file says; the file keeps its own idle and sleep choices, and a record
// that opens nothing is refused.
func TestAdopt(t *testing.T) {
	v, err := NewVerifier([]byte("correct horse"), cheap)
	if err != nil {
		t.Fatal(err)
	}
	mine := Config{Method: Password, Verifier: v}
	forged, err := NewVerifier([]byte("intruder pass"), cheap)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []Config{Default(), {Method: Password, Verifier: forged, IdleMinutes: 5}, {Method: TOTP}} {
		c := file
		changed, err := c.Adopt(mine.Record())
		if err != nil || !changed || c.Method != Password || !bytes.Equal(c.Verifier, v) || c.IdleMinutes != file.IdleMinutes {
			t.Fatalf("adopt over %+v: %+v %v %v", file, c, changed, err)
		}
	}
	c := mine
	if changed, err := c.Adopt(mine.Record()); err != nil || changed {
		t.Fatalf("same record: %v %v", changed, err)
	}
	for _, bad := range [][]byte{Default().Record(), (Config{Method: Password}).Record(), []byte("{")} {
		c := mine
		if _, err := c.Adopt(bad); err == nil || c.Method != Password {
			t.Fatalf("bad record %q adopted: %+v", bad, c)
		}
	}
}
