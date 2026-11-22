package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/middleware"
	"golang.org/x/crypto/bcrypt"
)

const (
	bcryptCost             = 10
	minNameRunes           = 2
	maxNameRunes           = 100
	minPasswordRunes       = 6
	maxBcryptPasswordBytes = 72
	maxEmailLength         = 254
	jwtTokenTTL            = 24 * time.Hour
	loginFailMax           = 5
	loginFailWindow        = 15 * time.Minute
	loginAcctKeyPrefix     = "rl:login:acct:"
	loginFailKeyPrefix     = "rl:login:fail:"
	unknownLoginClientIP   = "unknown"
	errMsgInvalidCreds     = "invalid email or password"
	errMsgInvalidAuth      = "invalid or expired token"
)

var dummyBcryptHash = mustDummyBcryptHash()

func mustDummyBcryptHash() []byte {
	hash, err := bcrypt.GenerateFromPassword([]byte("dummy-timing-parity-password"), bcryptCost)
	if err != nil {
		// Static valid cost-10 fallback if random entropy is unavailable at init.
		return []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")
	}
	return hash
}

// AuthService manages registration, login, and JWT issuance/verification.
type AuthService struct {
	users        UserRepository
	jwtSecret    []byte
	limiter      RateLimiter
	loginAcctMax int
}

// NewAuthService constructs an AuthService.
func NewAuthService(users UserRepository, jwtSecret string) *AuthService {
	return &AuthService{
		users:     users,
		jwtSecret: []byte(jwtSecret),
	}
}

// WithLoginRateLimiter enables fail-open email-keyed throttling and failed-attempt lockout before bcrypt runs.
func (s *AuthService) WithLoginRateLimiter(limiter RateLimiter, maxPerMinute int) *AuthService {
	if s != nil {
		s.limiter = limiter
		s.loginAcctMax = maxPerMinute
	}
	return s
}

// RegisterInput carries registration fields.
type RegisterInput struct {
	Email    string
	Name     string
	Password string
	Role     domain.Role
}

// AuthResult holds user details alongside a signed JWT token.
type AuthResult struct {
	User        *domain.User `json:"user"`
	AccessToken string       `json:"access_token"`
}

// Register creates a user with hashed credentials and issues a JWT token.
func (s *AuthService) Register(ctx context.Context, in RegisterInput) (*AuthResult, error) {
	if err := validateServiceCall(ctx, "registering user", s != nil && s.users != nil); err != nil {
		return nil, err
	}
	if !s.hasJWTSecret() {
		return nil, errors.New("registering user: jwt secret is not configured")
	}

	email, name, err := validateRegisterInput(in)
	if err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}

	u := &domain.User{
		ID:           uuid.New(),
		Email:        email,
		Name:         name,
		PasswordHash: string(hash),
		Role:         in.Role,
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}

	tok, err := s.signJWT(u)
	if err != nil {
		return nil, err
	}
	return &AuthResult{User: u, AccessToken: tok}, nil
}

// Login authenticates an existing user and returns a signed JWT token.
func (s *AuthService) Login(ctx context.Context, email, password string) (*AuthResult, error) {
	if err := validateServiceCall(ctx, "logging in user", s != nil && s.users != nil); err != nil {
		return nil, err
	}
	if !s.hasJWTSecret() {
		return nil, errors.New("logging in user: jwt secret is not configured")
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	if normalizedEmail == "" || strings.TrimSpace(password) == "" {
		return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeInvalidCredentials, errMsgInvalidCreds)
	}
	emailHash := loginEmailHash(normalizedEmail)
	if err := s.enforceLoginRateLimit(ctx, emailHash); err != nil {
		return nil, err
	}

	u, err := s.users.FindByEmail(ctx, normalizedEmail)
	if err != nil {
		if de, ok := errors.AsType[*domain.DomainError](err); ok && de.Code == domain.ErrCodeInvalidCredentials {
			_ = bcrypt.CompareHashAndPassword(dummyBcryptHash, []byte(password))
			s.recordFailedLogin(ctx, emailHash)
			return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeInvalidCredentials, errMsgInvalidCreds)
		}
		return nil, err
	}
	if u == nil || u.PasswordHash == "" {
		_ = bcrypt.CompareHashAndPassword(dummyBcryptHash, []byte(password))
		s.recordFailedLogin(ctx, emailHash)
		return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeInvalidCredentials, errMsgInvalidCreds)
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		s.recordFailedLogin(ctx, emailHash)
		return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeInvalidCredentials, errMsgInvalidCreds)
	}
	s.resetFailedLogin(ctx, emailHash)

	tok, err := s.signJWT(u)
	if err != nil {
		return nil, err
	}
	return &AuthResult{User: u, AccessToken: tok}, nil
}

// GetProfile retrieves the authenticated user's profile by ID.
func (s *AuthService) GetProfile(ctx context.Context, userID uuid.UUID) (*domain.User, error) {
	if err := validateServiceCall(ctx, "getting user profile", s != nil && s.users != nil); err != nil {
		return nil, err
	}
	if userID == uuid.Nil {
		return nil, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "authentication is required")
	}
	return s.users.FindByID(ctx, userID)
}

// ParseJWT validates a bearer token and extracts the authenticated Actor.
func (s *AuthService) ParseJWT(tokenStr string) (domain.Actor, error) {
	if !s.hasJWTSecret() {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, errMsgInvalidAuth)
	}

	trimmed := strings.TrimSpace(tokenStr)
	if trimmed == "" {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, errMsgInvalidAuth)
	}

	token, err := jwt.Parse(trimmed, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil || token == nil || !token.Valid {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, errMsgInvalidAuth)
	}

	return extractActorFromClaims(token.Claims)
}

// EnsureDemoUser delegates synthetic demo user creation to UserRepository.
func (s *AuthService) EnsureDemoUser(ctx context.Context, id uuid.UUID, role domain.Role) error {
	if err := validateServiceCall(ctx, "ensuring demo user", s != nil && s.users != nil); err != nil {
		return err
	}
	if id == uuid.Nil {
		return domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "user id is required")
	}
	if err := validateUserRole(role); err != nil {
		return err
	}
	return s.users.EnsureDemoUser(ctx, id, role)
}

func (s *AuthService) hasJWTSecret() bool {
	return s != nil && len(bytes.TrimSpace(s.jwtSecret)) > 0
}

func loginEmailHash(normalizedEmail string) string {
	sum := sha256.Sum256([]byte(normalizedEmail))
	return hex.EncodeToString(sum[:8])
}

func loginFailKey(ctx context.Context, emailHash string) string {
	ip := strings.TrimSpace(middleware.ClientIPFromContext(ctx))
	if ip == "" {
		ip = unknownLoginClientIP
	}
	return loginFailKeyPrefix + ip + ":" + emailHash
}

func (s *AuthService) enforceLoginRateLimit(ctx context.Context, emailHash string) error {
	if s.limiter == nil {
		return nil
	}
	if peeker, ok := s.limiter.(RateLimitPeeker); ok {
		allowed, retryAfter, err := peeker.Peek(ctx, loginFailKey(ctx, emailHash), loginFailMax)
		if err == nil && !allowed {
			return domain.NewRateLimitedError("too many failed login attempts; please wait and try again").WithRetryAfter(retryAfter)
		}
	}
	if s.loginAcctMax <= 0 {
		return nil
	}
	allowed, retryAfter, err := s.limiter.Allow(ctx, loginAcctKeyPrefix+emailHash, s.loginAcctMax, time.Minute)
	if err == nil && !allowed {
		return domain.NewRateLimitedError("too many login attempts; please wait and try again").WithRetryAfter(retryAfter)
	}
	// Fail open when limiter store errors so login continues uninterrupted.
	return nil
}

func (s *AuthService) recordFailedLogin(ctx context.Context, emailHash string) {
	if s.limiter == nil {
		return
	}
	if _, _, err := s.limiter.Allow(ctx, loginFailKey(ctx, emailHash), loginFailMax, loginFailWindow); err != nil {
		return
	}
}

func (s *AuthService) resetFailedLogin(ctx context.Context, emailHash string) {
	if resetter, ok := s.limiter.(RateLimitResetter); ok {
		if err := resetter.Reset(ctx, loginFailKey(ctx, emailHash)); err != nil {
			return
		}
	}
}

func (s *AuthService) signJWT(u *domain.User) (string, error) {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub":   u.ID.String(),
		"email": u.Email,
		"role":  string(u.Role),
		"iat":   now.Unix(),
		"exp":   now.Add(jwtTokenTTL).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("signing jwt: %w", err)
	}
	return signed, nil
}

func validateRegisterInput(in RegisterInput) (string, string, error) {
	if err := validateUserRole(in.Role); err != nil {
		return "", "", err
	}

	email, err := normalizeAndValidateEmail(in.Email)
	if err != nil {
		return "", "", err
	}

	name := strings.TrimSpace(in.Name)
	nameLen := utf8.RuneCountInString(name)
	if nameLen < minNameRunes || nameLen > maxNameRunes {
		return "", "", domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "name must be between 2 and 100 characters")
	}

	if err := validatePassword(in.Password); err != nil {
		return "", "", err
	}
	return email, name, nil
}

func normalizeAndValidateEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > maxEmailLength {
		return "", domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "valid email is required")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email || !strings.Contains(addr.Address, "@") {
		return "", domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "valid email is required")
	}
	return email, nil
}

func validatePassword(password string) error {
	if strings.TrimSpace(password) == "" || utf8.RuneCountInString(password) < minPasswordRunes {
		return domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "password must be at least 6 characters")
	}
	if len(password) > maxBcryptPasswordBytes {
		return domain.NewDomainError(http.StatusUnprocessableEntity, domain.ErrCodeValidation, "password must not exceed 72 bytes")
	}
	return nil
}

func extractActorFromClaims(rawClaims jwt.Claims) (domain.Actor, error) {
	claims, ok := rawClaims.(jwt.MapClaims)
	if !ok {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "invalid token claims")
	}
	subStr, ok := claims["sub"].(string)
	if !ok {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "invalid subject UUID")
	}
	uid, err := uuid.Parse(subStr)
	if err != nil || uid == uuid.Nil {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "invalid subject UUID")
	}

	roleStr, ok := claims["role"].(string)
	if !ok {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "invalid token role")
	}
	role := domain.Role(roleStr)
	if role != domain.RoleOrganizer && role != domain.RoleBuyer {
		return domain.Actor{}, domain.NewDomainError(http.StatusUnauthorized, domain.ErrCodeUnauthorized, "invalid token role")
	}

	var emailStr string
	if rawEmail, hasEmail := claims["email"].(string); hasEmail {
		emailStr = rawEmail
	}
	return domain.Actor{ID: uid, Email: emailStr, Role: role}, nil
}
