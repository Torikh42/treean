package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/torikh42/treean/internal/service"
)

const demoTag = "Demo"

// FlashSaleBody configures an optional burst size and ticket quota for the concurrency benchmark.
type FlashSaleBody struct {
	Requests int   `json:"requests,omitempty" minimum:"1" maximum:"500" default:"200" example:"200"`
	Quota    int32 `json:"quota,omitempty" minimum:"1" maximum:"500" default:"100" example:"100"`
}

func registerDemoRoutes(api huma.API, logger *slog.Logger, demoSvc *service.DemoService) {
	huma.Register(api, huma.Operation{
		OperationID: "demo-flash-sale",
		Method:      http.MethodPost,
		Path:        "/api/v1/demo/flash-sale",
		Summary:     "Execute concurrent buyer spike benchmark (default: 200 requests vs 100 quota)",
		Tags:        []string{demoTag},
	}, func(ctx context.Context, in *struct{ Body *FlashSaleBody }) (*struct{ Body *service.FlashSaleReport }, error) {
		var requests int
		var quota int32
		if in != nil && in.Body != nil {
			requests = in.Body.Requests
			quota = in.Body.Quota
		}
		report, err := demoSvc.RunFlashSale(ctx, requests, quota)
		if err != nil {
			return nil, mapServiceError(ctx, logger, err)
		}
		return &struct{ Body *service.FlashSaleReport }{Body: report}, nil
	})
}
