package handler

import (
	"context"
	"log/slog"

	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/service"
)

// RegisterBody defines the JSON request body for user account registration.
type RegisterBody struct {
	Email    string `json:"email" format:"email" doc:"Unique email address"`
	Name     string `json:"name" minLength:"2" maxLength:"100" doc:"Full name"`
	Password string `json:"password" minLength:"6" maxLength:"72" doc:"Account password"`
	Role     string `json:"role" enum:"ORGANIZER,BUYER" doc:"User role"`
}

// RegisterOutput wraps the registration response payload.
type RegisterOutput struct {
	Body *service.AuthResult
}

// LoginBody defines the JSON request body for credential login.
type LoginBody struct {
	Email    string `json:"email" format:"email" doc:"Registered email"`
	Password string `json:"password" minLength:"1" doc:"Account password"`
}

// LoginOutput wraps the login response payload.
type LoginOutput struct {
	Body *service.AuthResult
}

// MeOutput wraps the authenticated user profile response payload.
type MeOutput struct {
	Body *domain.User
}

// AuthHandler handles registration, credential login, and profile endpoints.
type AuthHandler struct {
	svc    *service.AuthService
	logger *slog.Logger
}

// NewAuthHandler constructs an AuthHandler.
func NewAuthHandler(svc *service.AuthService, logger *slog.Logger) *AuthHandler {
	return &AuthHandler{svc: svc, logger: SafeLogger(logger)}
}

// Register creates an organizer or buyer user and issues a signed Bearer JWT.
func (h *AuthHandler) Register(ctx context.Context, in *struct{ Body RegisterBody }) (*RegisterOutput, error) {
	res, err := h.svc.Register(ctx, service.RegisterInput{
		Email:    in.Body.Email,
		Name:     in.Body.Name,
		Password: in.Body.Password,
		Role:     domain.Role(in.Body.Role),
	})
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &RegisterOutput{Body: res}, nil
}

// Login verifies user credentials and issues a signed Bearer JWT.
func (h *AuthHandler) Login(ctx context.Context, in *struct{ Body LoginBody }) (*LoginOutput, error) {
	res, err := h.svc.Login(ctx, in.Body.Email, in.Body.Password)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &LoginOutput{Body: res}, nil
}

// Me returns the profile of the authenticated caller.
func (h *AuthHandler) Me(ctx context.Context, _ *struct{}) (*MeOutput, error) {
	actor, err := RequireActor(ctx)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	user, err := h.svc.GetProfile(ctx, actor.ID)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &MeOutput{Body: user}, nil
}
