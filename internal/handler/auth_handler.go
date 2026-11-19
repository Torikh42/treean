package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/treean/internal/domain"
	"github.com/torikh42/treean/internal/service"
)

const authTag = "Auth"

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

func registerAuthRoutes(api huma.API, logger *slog.Logger, authSvc *service.AuthService) {
	huma.Register(api, huma.Operation{
		OperationID:   "register-user",
		Method:        http.MethodPost,
		Path:          "/api/v1/auth/register",
		Summary:       "Register an organizer or buyer account",
		Tags:          []string{authTag},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct{ Body RegisterBody }) (*RegisterOutput, error) {
		res, err := authSvc.Register(ctx, service.RegisterInput{
			Email:    in.Body.Email,
			Name:     in.Body.Name,
			Password: in.Body.Password,
			Role:     domain.Role(in.Body.Role),
		})
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &RegisterOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "login-user",
		Method:      http.MethodPost,
		Path:        "/api/v1/auth/login",
		Summary:     "Login with email and password to receive a Bearer JWT",
		Tags:        []string{authTag},
	}, func(ctx context.Context, in *struct{ Body LoginBody }) (*LoginOutput, error) {
		res, err := authSvc.Login(ctx, in.Body.Email, in.Body.Password)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &LoginOutput{Body: res}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-me",
		Method:      http.MethodGet,
		Path:        "/api/v1/auth/me",
		Summary:     "Get authenticated user profile",
		Tags:        []string{authTag},
		Security:    bearerSecurity,
	}, func(ctx context.Context, _ *struct{}) (*MeOutput, error) {
		actor, err := requireActor(ctx)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		user, err := authSvc.GetProfile(ctx, actor.ID)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &MeOutput{Body: user}, nil
	})
}
