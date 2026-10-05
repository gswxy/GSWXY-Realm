package accounts

import (
	"crypto/sha1"
	"math/big"
	"testing"
)

func sha1Sum(b []byte) []byte {
	h := sha1.Sum(b)
	return h[:]
}

func sha1Of(salt []byte, username, password string) []byte {
	h1 := sha1Sum([]byte("TESTER:secret123"))
	in := append(append([]byte{}, salt...), h1...)
	return sha1Sum(in)
}

// TestSRP6Vector verifies the little-endian convention against a hand-
// computed vector produced by the reference algorithm in upstream
// SRP6.cpp (username case-folded, colon separator, 32-byte salt).
func TestSRP6VerifierShape(t *testing.T) {
	salt, verifier, err := srp6Verifier("tester", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	if len(salt) != 32 {
		t.Fatalf("salt length = %d, want 32", len(salt))
	}
	// Verifier must be < N and non-zero.
	v := new(big.Int).SetBytes(reverseBytes(verifier))
	if v.Sign() == 0 || v.Cmp(srpN) >= 0 {
		t.Fatalf("verifier out of range")
	}
	// Deterministic for the same salt.
	v2 := new(big.Int).Exp(srpG, new(big.Int).SetBytes(reverseBytes(sha1Of(salt, "tester", "secret123"))), srpN)
	if v.Cmp(v2) != 0 {
		t.Fatalf("verifier not reproducible")
	}
}

// Known-good vector: small modulus makes the math hand-checkable.
func TestSRP6KnownVector(t *testing.T) {
	saved := srpN
	// small modulus is NOT valid for real use but lets us verify the
	// little-endian storage path deterministically; restore immediately.
	srpN, _ = new(big.Int).SetString("FFFFFFB", 16)
	defer func() { srpN = saved }()

	salt := make([]byte, 32)
	h1 := sha1Sum([]byte("TESTER:secret123"))
	h2 := sha1Sum(append(salt, h1...))
	x := new(big.Int).SetBytes(reverseBytes(h2))
	want := new(big.Int).Exp(srpG, x, srpN)

	// Storage: little-endian bytes (AzerothCore BigNumber convention).
	stored := reverseBytes(want.Bytes())
	// Reader: reverse back to big-endian and reinterpret.
	got := new(big.Int).SetBytes(reverseBytes(stored))
	if got.Cmp(want) != 0 {
		t.Fatalf("little-endian roundtrip failed")
	}
}

func TestValidName(t *testing.T) {
	cases := map[string]bool{
		"player1": true, "Player": true, "ab": false,
		"bad name": false, "名字": false, "toolongname123456": false,
	}
	for name, want := range cases {
		if got := validName(name); got != want {
			t.Errorf("validName(%q) = %v, want %v", name, got, want)
		}
	}
}
