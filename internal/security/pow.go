// Package security provides stateless cryptographic anti-abuse primitives such as Proof-of-Work challenges.
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	powVersion           = "v1"
	minDifficulty        = 8
	maxDifficulty        = 24
	saltByteLen          = 16
	maxSolutionHeaderLen = 512
	maxNonceLen          = 64
	nonceKeyPrefix       = "pow:used:"
)

var (
	// ErrInvalidPoWConfig indicates missing or out-of-range PoW verifier configuration.
	ErrInvalidPoWConfig = errors.New("invalid proof-of-work configuration")
	// ErrInvalidPoWChallenge indicates a malformed, tampered, or wrong-IP challenge token.
	ErrInvalidPoWChallenge = errors.New("invalid proof-of-work challenge")
	// ErrExpiredPoWChallenge indicates a challenge token past its expiry.
	ErrExpiredPoWChallenge = errors.New("expired proof-of-work challenge")
	// ErrInvalidPoWSolution indicates a malformed header or nonce lacking the required leading zero bits.
	ErrInvalidPoWSolution = errors.New("invalid proof-of-work solution")
)

// PoWVerifier issues and verifies stateless HMAC-SHA256 Proof-of-Work challenges bound to a client IP and expiry.
type PoWVerifier struct {
	secret     []byte
	difficulty int
	ttl        time.Duration
}

// NewPoWVerifier constructs a fail-closed PoWVerifier, rejecting blank secrets, out-of-range difficulties, or non-positive TTLs.
func NewPoWVerifier(secret string, difficulty int, ttl time.Duration) (*PoWVerifier, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, fmt.Errorf("%w: secret must not be blank", ErrInvalidPoWConfig)
	}
	if difficulty < minDifficulty || difficulty > maxDifficulty {
		return nil, fmt.Errorf("%w: difficulty %d must be between %d and %d", ErrInvalidPoWConfig, difficulty, minDifficulty, maxDifficulty)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("%w: ttl must be positive", ErrInvalidPoWConfig)
	}

	return &PoWVerifier{
		secret:     []byte(secret),
		difficulty: difficulty,
		ttl:        ttl,
	}, nil
}

// Issue generates a signed Proof-of-Work challenge token bound to clientIP and expiring at now + ttl (UTC).
func (v *PoWVerifier) Issue(clientIP string, now time.Time) (string, error) {
	if v == nil || len(v.secret) == 0 {
		return "", fmt.Errorf("%w: verifier is not initialized", ErrInvalidPoWConfig)
	}
	ip := strings.TrimSpace(clientIP)
	if ip == "" {
		return "", fmt.Errorf("%w: client IP is required", ErrInvalidPoWChallenge)
	}
	utcNow := normalizeUTC(now)

	salt := make([]byte, saltByteLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating pow salt: %w", err)
	}

	expiresStr := strconv.FormatInt(utcNow.Add(v.ttl).Unix(), 10)
	diffStr := strconv.Itoa(v.difficulty)
	saltHex := hex.EncodeToString(salt)
	macHex := hex.EncodeToString(v.computeMAC(expiresStr, diffStr, saltHex, ip))

	return powVersion + "." + expiresStr + "." + diffStr + "." + saltHex + "." + macHex, nil
}

// Verify validates a "<challengeToken>:<nonceStr>" header against clientIP and now (UTC), returning a replay-guard Redis key.
func (v *PoWVerifier) Verify(solutionHeader string, clientIP string, now time.Time) (string, error) {
	if v == nil || len(v.secret) == 0 {
		return "", fmt.Errorf("%w: verifier is not initialized", ErrInvalidPoWConfig)
	}
	ip := strings.TrimSpace(clientIP)
	if ip == "" {
		return "", fmt.Errorf("%w: client IP is required", ErrInvalidPoWChallenge)
	}
	if len(solutionHeader) == 0 || len(solutionHeader) > maxSolutionHeaderLen {
		return "", fmt.Errorf("%w: solution header length out of bounds", ErrInvalidPoWSolution)
	}

	challengeToken, nonceStr, ok := strings.Cut(solutionHeader, ":")
	if !ok || len(nonceStr) == 0 || len(nonceStr) > maxNonceLen || strings.TrimSpace(nonceStr) == "" {
		return "", fmt.Errorf("%w: malformed solution header", ErrInvalidPoWSolution)
	}

	expectedMAC, err := v.parseAndVerifyChallengeToken(challengeToken, ip, normalizeUTC(now))
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256([]byte(challengeToken + ":" + nonceStr))
	if !hasLeadingZeroBits(sum[:], v.difficulty) {
		return "", fmt.Errorf("%w: insufficient leading zero bits", ErrInvalidPoWSolution)
	}

	macDigest := sha256.Sum256(expectedMAC)
	return nonceKeyPrefix + hex.EncodeToString(macDigest[:]), nil
}

func (v *PoWVerifier) parseAndVerifyChallengeToken(challengeToken, ip string, utcNow time.Time) ([]byte, error) {
	parts := strings.Split(challengeToken, ".")
	if len(parts) != 5 || parts[0] != powVersion {
		return nil, fmt.Errorf("%w: malformed challenge token", ErrInvalidPoWChallenge)
	}
	expiresStr, diffStr, saltHex, macHex := parts[1], parts[2], parts[3], parts[4]

	expiresUnix, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid expiry", ErrInvalidPoWChallenge)
	}
	diff, err := strconv.Atoi(diffStr)
	if err != nil || diff != v.difficulty {
		return nil, fmt.Errorf("%w: unexpected difficulty", ErrInvalidPoWChallenge)
	}
	if _, err := decodeLowerHex(saltHex, saltByteLen); err != nil {
		return nil, fmt.Errorf("%w: invalid salt encoding", ErrInvalidPoWChallenge)
	}
	gotMAC, err := decodeLowerHex(macHex, sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid mac encoding", ErrInvalidPoWChallenge)
	}

	expectedMAC := v.computeMAC(expiresStr, diffStr, saltHex, ip)
	if !hmac.Equal(gotMAC, expectedMAC) {
		return nil, fmt.Errorf("%w: mac verification failed", ErrInvalidPoWChallenge)
	}
	if expiresUnix < utcNow.Unix() {
		return nil, ErrExpiredPoWChallenge
	}
	return expectedMAC, nil
}

func decodeLowerHex(raw string, expectedBytes int) ([]byte, error) {
	if len(raw) != expectedBytes*2 || !isLowerHex(raw) {
		return nil, ErrInvalidPoWChallenge
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != expectedBytes {
		return nil, ErrInvalidPoWChallenge
	}
	return decoded, nil
}

func isLowerHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (v *PoWVerifier) computeMAC(expiresStr, diffStr, saltHex, clientIP string) []byte {
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(powVersion + "|" + expiresStr + "|" + diffStr + "|" + saltHex + "|" + clientIP))
	return mac.Sum(nil)
}

func normalizeUTC(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now.UTC()
}

func hasLeadingZeroBits(digest []byte, bits int) bool {
	if bits < 0 || bits > len(digest)*8 {
		return false
	}
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
