package webhook

import (
	"strings"
	"testing"
	"time"
)

func TestSign_RoundTripVerifies(t *testing.T) {
	secret := []byte("super-secret")
	body := []byte(`{"event":"intent.completed"}`)
	now := time.Unix(1_700_000_000, 0)

	header, err := Sign(secret, body, now)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !strings.HasPrefix(header, "t=1700000000,") {
		t.Errorf("header missing timestamp prefix: %q", header)
	}
	if err := Verify(secret, body, header, now, time.Minute); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestSign_EmptySecretRejected(t *testing.T) {
	if _, err := Sign(nil, []byte("body"), time.Now()); err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestVerify_RejectsTamperedBody(t *testing.T) {
	secret := []byte("k")
	now := time.Unix(1, 0)
	header, _ := Sign(secret, []byte("original"), now)
	if err := Verify(secret, []byte("tampered"), header, now, time.Minute); err == nil {
		t.Fatal("expected mismatch error for tampered body")
	}
}

func TestVerify_RejectsWrongSecret(t *testing.T) {
	now := time.Unix(1, 0)
	header, _ := Sign([]byte("alpha"), []byte("body"), now)
	if err := Verify([]byte("beta"), []byte("body"), header, now, time.Minute); err == nil {
		t.Fatal("expected mismatch error for wrong secret")
	}
}

func TestVerify_RejectsStaleTimestamp(t *testing.T) {
	secret := []byte("k")
	signed := time.Unix(1_000_000_000, 0)
	header, _ := Sign(secret, []byte("b"), signed)

	verified := signed.Add(10 * time.Minute)
	if err := Verify(secret, []byte("b"), header, verified, 5*time.Minute); err == nil {
		t.Fatal("expected stale-timestamp error")
	}
	// Same age but tolerance disabled should pass.
	if err := Verify(secret, []byte("b"), header, verified, 0); err != nil {
		t.Errorf("verify with tolerance=0: %v", err)
	}
}

func TestVerify_RejectsBadHeader(t *testing.T) {
	cases := []string{
		"",
		"v1=abc",
		"t=notnum,v1=abc",
		"t=1,v1=zzzz",
		"t=1",
	}
	for _, c := range cases {
		if err := Verify([]byte("k"), []byte("b"), c, time.Unix(1, 0), 0); err == nil {
			t.Errorf("expected error for header %q", c)
		}
	}
}

func TestVerify_AcceptsMultipleSchemes(t *testing.T) {
	secret := []byte("k")
	now := time.Unix(1_700_000_000, 0)
	good, _ := Sign(secret, []byte("body"), now)
	// Receivers may carry forward an old v1 alongside a future v2;
	// our parser must still pick out v1 from a comma-separated list.
	withExtra := good + ",v2=deadbeef"
	if err := Verify(secret, []byte("body"), withExtra, now, time.Minute); err != nil {
		t.Fatalf("verify with extra scheme: %v", err)
	}
}
