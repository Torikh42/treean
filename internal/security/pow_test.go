package security_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/torikh42/treean/internal/security"
)

func solveTestChallenge(t *testing.T, challenge string, difficulty int) string {
	t.Helper()
	for i := range 10_000_000 {
		nonce := fmt.Sprintf("%x", i)
		sum := sha256.Sum256([]byte(challenge + ":" + nonce))
		if digestHasLeadingZeroBits(sum[:], difficulty) {
			return nonce
		}
	}
	t.Fatalf("failed to solve challenge %q at difficulty %d", challenge, difficulty)
	return ""
}

func digestHasLeadingZeroBits(digest []byte, bits int) bool {
	fullBytes := bits / 8
	for i := range fullBytes {
		if digest[i] != 0 {
			return false
		}
	}
	remBits := bits % 8
	if remBits == 0 {
		return true
	}
	mask := byte(0xFF << (8 - remBits))
	return (digest[fullBytes] & mask) == 0
}

func TestPoWVerifier_ValidSolvePasses(t *testing.T) {
	t.Parallel()

	const difficulty = 10
	ttl := 5 * time.Minute
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	v, err := security.NewPoWVerifier("super-secret-pow-key", difficulty, ttl)
	if err != nil {
		t.Fatalf("NewPoWVerifier: %v", err)
	}

	challenge, err := v.Issue("203.0.113.10", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	parts := strings.Split(challenge, ".")
	if len(parts) != 5 || parts[0] != "v1" {
		t.Fatalf("unexpected challenge format: %q", challenge)
	}

	nonce := solveTestChallenge(t, challenge, difficulty)
	header := challenge + ":" + nonce

	nonceKey, err := v.Verify(header, "203.0.113.10", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Verify valid solution failed: %v", err)
	}
	if !strings.HasPrefix(nonceKey, "pow:used:") || len(nonceKey) <= len("pow:used:") {
		t.Fatalf("unexpected nonceKey %q", nonceKey)
	}

	// Exact expiration boundary (expiresUnix == now.Unix()) must remain valid.
	nonceKeyBoundary, err := v.Verify(header, " 203.0.113.10 ", now.Add(ttl))
	if err != nil {
		t.Fatalf("Verify at exact expiry boundary failed: %v", err)
	}
	if nonceKeyBoundary != nonceKey {
		t.Fatalf("expected deterministic nonceKey %q, got %q", nonceKey, nonceKeyBoundary)
	}
}

func TestPoWVerifier_RejectsBlankSecretAndInvalidDifficulty(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		secret     string
		difficulty int
		ttl        time.Duration
		wantErr    bool
	}{
		{name: "empty secret", secret: "", difficulty: 16, ttl: time.Minute, wantErr: true},
		{name: "whitespace secret", secret: "   \t\n", difficulty: 16, ttl: time.Minute, wantErr: true},
		{name: "difficulty below min", secret: "secret", difficulty: 7, ttl: time.Minute, wantErr: true},
		{name: "difficulty zero", secret: "secret", difficulty: 0, ttl: time.Minute, wantErr: true},
		{name: "difficulty negative", secret: "secret", difficulty: -1, ttl: time.Minute, wantErr: true},
		{name: "difficulty above max", secret: "secret", difficulty: 25, ttl: time.Minute, wantErr: true},
		{name: "zero ttl", secret: "secret", difficulty: 16, ttl: 0, wantErr: true},
		{name: "negative ttl", secret: "secret", difficulty: 16, ttl: -time.Second, wantErr: true},
		{name: "min difficulty valid", secret: "secret", difficulty: 8, ttl: time.Minute, wantErr: false},
		{name: "max difficulty valid", secret: "secret", difficulty: 24, ttl: time.Minute, wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v, err := security.NewPoWVerifier(tc.secret, tc.difficulty, tc.ttl)
			if tc.wantErr {
				if err == nil || v != nil {
					t.Fatalf("expected error and nil verifier, got (%v, %v)", v, err)
				}
				return
			}
			if err != nil || v == nil {
				t.Fatalf("expected valid verifier, got (%v, %v)", v, err)
			}
		})
	}
}

func TestPoWVerifier_RejectsWrongIPExpiredAndTamperedMAC(t *testing.T) {
	t.Parallel()

	const difficulty = 8
	ttl := 5 * time.Minute
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	v, err := security.NewPoWVerifier("test-secret", difficulty, ttl)
	if err != nil {
		t.Fatalf("NewPoWVerifier: %v", err)
	}

	if _, err := v.Issue("", now); err == nil {
		t.Fatal("expected error when issuing for empty clientIP")
	}
	if _, err := v.Issue("   ", now); err == nil {
		t.Fatal("expected error when issuing for blank clientIP")
	}

	challenge, err := v.Issue("203.0.113.10", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	nonce := solveTestChallenge(t, challenge, difficulty)
	validHeader := challenge + ":" + nonce

	parts := strings.Split(challenge, ".")
	if len(parts) != 5 {
		t.Fatalf("unexpected challenge parts: %v", parts)
	}

	// Tamper difficulty
	tamperedDiff := strings.Join([]string{parts[0], parts[1], "9", parts[3], parts[4]}, ".")
	// Tamper salt
	saltBytes, err := hex.DecodeString(parts[3])
	if err != nil {
		t.Fatalf("DecodeString salt: %v", err)
	}
	saltBytes[0] ^= 0xFF
	tamperedSalt := strings.Join([]string{parts[0], parts[1], parts[2], hex.EncodeToString(saltBytes), parts[4]}, ".")
	// Tamper MAC
	macBytes, err := hex.DecodeString(parts[4])
	if err != nil {
		t.Fatalf("DecodeString mac: %v", err)
	}
	macBytes[0] ^= 0xFF
	tamperedMAC := strings.Join([]string{parts[0], parts[1], parts[2], parts[3], hex.EncodeToString(macBytes)}, ".")

	otherVerifier, err := security.NewPoWVerifier("different-secret", difficulty, ttl)
	if err != nil {
		t.Fatalf("NewPoWVerifier other: %v", err)
	}

	var nilVerifier *security.PoWVerifier
	if _, err := nilVerifier.Issue("203.0.113.10", now); err == nil {
		t.Fatal("expected error on nil verifier Issue")
	}
	if _, err := nilVerifier.Verify(validHeader, "203.0.113.10", now); err == nil {
		t.Fatal("expected error on nil verifier Verify")
	}

	cases := []struct {
		name     string
		verifier *security.PoWVerifier
		header   string
		ip       string
		verifyAt time.Time
	}{
		{name: "blank clientIP", verifier: v, header: validHeader, ip: "", verifyAt: now},
		{name: "whitespace clientIP", verifier: v, header: validHeader, ip: "   ", verifyAt: now},
		{name: "mismatched IP", verifier: v, header: validHeader, ip: "203.0.113.99", verifyAt: now},
		{name: "expired challenge", verifier: v, header: validHeader, ip: "203.0.113.10", verifyAt: now.Add(ttl + time.Second)},
		{name: "tampered difficulty", verifier: v, header: tamperedDiff + ":" + solveTestChallenge(t, tamperedDiff, 9), ip: "203.0.113.10", verifyAt: now},
		{name: "tampered salt", verifier: v, header: tamperedSalt + ":" + nonce, ip: "203.0.113.10", verifyAt: now},
		{name: "tampered MAC", verifier: v, header: tamperedMAC + ":" + nonce, ip: "203.0.113.10", verifyAt: now},
		{name: "wrong verifier secret", verifier: otherVerifier, header: validHeader, ip: "203.0.113.10", verifyAt: now},
		{name: "missing nonce", verifier: v, header: challenge + ":", ip: "203.0.113.10", verifyAt: now},
		{name: "missing separator", verifier: v, header: challenge, ip: "203.0.113.10", verifyAt: now},
		{name: "oversized nonce", verifier: v, header: challenge + ":" + strings.Repeat("a", 65), ip: "203.0.113.10", verifyAt: now},
		{name: "oversized header", verifier: v, header: validHeader + strings.Repeat("a", 512), ip: "203.0.113.10", verifyAt: now},
		{name: "bad version", verifier: v, header: "v2." + strings.Join(parts[1:], ".") + ":" + nonce, ip: "203.0.113.10", verifyAt: now},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nonceKey, err := tc.verifier.Verify(tc.header, tc.ip, tc.verifyAt)
			if err == nil || nonceKey != "" {
				t.Fatalf("expected verification failure, got (%q, %v)", nonceKey, err)
			}
		})
	}
}

func TestPoWVerifier_RejectsInsufficientLeadingZeros(t *testing.T) {
	t.Parallel()

	const difficulty = 12
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	v, err := security.NewPoWVerifier("test-secret", difficulty, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewPoWVerifier: %v", err)
	}

	challenge, err := v.Issue("198.51.100.7", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Find a nonce that meets 8 zero bits but fails 12 zero bits.
	var nearMissNonce string
	for i := range 100_000 {
		nonce := fmt.Sprintf("%x", i)
		sum := sha256.Sum256([]byte(challenge + ":" + nonce))
		if digestHasLeadingZeroBits(sum[:], 8) && !digestHasLeadingZeroBits(sum[:], difficulty) {
			nearMissNonce = nonce
			break
		}
	}
	if nearMissNonce == "" {
		t.Fatal("failed to find near-miss nonce")
	}

	if nonceKey, err := v.Verify(challenge+":"+nearMissNonce, "198.51.100.7", now); err == nil || nonceKey != "" {
		t.Fatalf("expected insufficient leading zeros error, got (%q, %v)", nonceKey, err)
	}
}

func TestPoWVerifier_RejectsNonCanonicalHexCasing(t *testing.T) {
	t.Parallel()

	const difficulty = 8
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	v, err := security.NewPoWVerifier("test-secret", difficulty, 5*time.Minute)
	if err != nil {
		t.Fatalf("NewPoWVerifier: %v", err)
	}

	var challenge string
	var parts []string
	for range 10 {
		challenge, err = v.Issue("203.0.113.10", now)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		parts = strings.Split(challenge, ".")
		if len(parts) == 5 && strings.ToUpper(parts[3]) != parts[3] && strings.ToUpper(parts[4]) != parts[4] {
			break
		}
	}
	if len(parts) != 5 || strings.ToUpper(parts[3]) == parts[3] || strings.ToUpper(parts[4]) == parts[4] {
		t.Fatal("failed to generate challenge with hex letters in salt and mac")
	}

	// Mixed-case macHex must be rejected even when the PoW nonce is freshly solved for the uppercased token.
	upperMACChallenge := strings.Join([]string{parts[0], parts[1], parts[2], parts[3], strings.ToUpper(parts[4])}, ".")
	upperMACNonce := solveTestChallenge(t, upperMACChallenge, difficulty)
	if nonceKey, err := v.Verify(upperMACChallenge+":"+upperMACNonce, "203.0.113.10", now); err == nil || nonceKey != "" {
		t.Fatalf("expected non-canonical uppercase macHex to be rejected, got (%q, %v)", nonceKey, err)
	}

	// Mixed-case saltHex must also be rejected.
	upperSaltChallenge := strings.Join([]string{parts[0], parts[1], parts[2], strings.ToUpper(parts[3]), parts[4]}, ".")
	upperSaltNonce := solveTestChallenge(t, upperSaltChallenge, difficulty)
	if nonceKey, err := v.Verify(upperSaltChallenge+":"+upperSaltNonce, "203.0.113.10", now); err == nil || nonceKey != "" {
		t.Fatalf("expected non-canonical uppercase saltHex to be rejected, got (%q, %v)", nonceKey, err)
	}
}
