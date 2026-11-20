package routes

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/treean/internal/handler"
)

const authTag = "Auth"

func registerAuthRoutes(api huma.API, h *handler.AuthHandler) {
	if h == nil {
		return
	}

	huma.Register(api, huma.Operation{
		OperationID:   "register-user",
		Method:        http.MethodPost,
		Path:          "/api/v1/auth/register",
		Summary:       "Register an organizer or buyer account",
		Tags:          []string{authTag},
		DefaultStatus: http.StatusCreated,
	}, h.Register)

	huma.Register(api, huma.Operation{
		OperationID: "login-user",
		Method:      http.MethodPost,
		Path:        "/api/v1/auth/login",
		Summary:     "Login with email and password to receive a Bearer JWT",
		Tags:        []string{authTag},
	}, h.Login)

	huma.Register(api, huma.Operation{
		OperationID: "get-me",
		Method:      http.MethodGet,
		Path:        "/api/v1/auth/me",
		Summary:     "Get authenticated user profile",
		Tags:        []string{authTag},
		Security:    bearerSecurity,
	}, h.Me)
}
