package routes

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/treean/internal/handler"
)

const demoTag = "Demo"

func registerDemoRoutes(api huma.API, h *handler.DemoHandler) {
	if h == nil {
		return
	}

	huma.Register(api, huma.Operation{
		OperationID: "demo-flash-sale",
		Method:      http.MethodPost,
		Path:        "/api/v1/demo/flash-sale",
		Summary:     "Execute concurrent buyer spike benchmark (default: 200 requests vs 100 quota)",
		Tags:        []string{demoTag},
	}, h.RunFlashSale)
}
