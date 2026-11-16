package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestPassword(t *testing.T) {
	h1, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := HashPassword("correct horse")
	if h1 == h2 {
		t.Error("two hashes of one password must differ (random salt)")
	}
	if !strings.HasPrefix(h1, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("unexpected format: %s", h1)
	}
	if !VerifyPassword(h1, "correct horse") {
		t.Error("right password rejected")
	}
	for _, bad := range []string{"correct horsE", "", "correct horse "} {
		if VerifyPassword(h1, bad) {
			t.Errorf("wrong password %q accepted", bad)
		}
	}
	for _, broken := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$a2V5", "$argon2id$v=19$m=19456,t=2,p=1$!!$!!", h1[:len(h1)-44] + "$"} {
		if VerifyPassword(broken, "correct horse") {
			t.Errorf("malformed hash %q accepted", broken)
		}
	}
	DummyVerify("anything") // must not panic
}

func TestToken(t *testing.T) {
	a, ha, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, hb, _ := NewToken()
	if a == b || bytes.Equal(ha, hb) {
		t.Fatal("tokens must be unique")
	}
	if len(a) != 43 || len(ha) != 32 {
		t.Errorf("token length %d, hash length %d", len(a), len(ha))
	}
	if !bytes.Equal(HashToken(a), ha) || bytes.Equal(HashToken(a+"x"), ha) {
		t.Error("HashToken must be deterministic and input-sensitive")
	}
}
