package handler

import (
	"context"
	"log/slog"

	"github.com/torikh42/treean/internal/service"
)

// FlashSaleBody configures an optional burst size and ticket quota for the concurrency benchmark.
type FlashSaleBody struct {
	Requests int   `json:"requests,omitempty" minimum:"1" maximum:"500" default:"200" example:"200"`
	Quota    int32 `json:"quota,omitempty" minimum:"1" maximum:"500" default:"100" example:"100"`
}

// DemoHandler handles demo benchmark endpoints such as the concurrent flash-sale stress test.
type DemoHandler struct {
	svc    *service.DemoService
	logger *slog.Logger
}

// NewDemoHandler constructs a DemoHandler.
func NewDemoHandler(svc *service.DemoService, logger *slog.Logger) *DemoHandler {
	return &DemoHandler{svc: svc, logger: SafeLogger(logger)}
}

// RunFlashSale executes parallel booking requests against finite quota to verify zero oversell.
func (h *DemoHandler) RunFlashSale(ctx context.Context, in *struct{ Body *FlashSaleBody }) (*struct{ Body *service.FlashSaleReport }, error) {
	var requests int
	var quota int32
	if in != nil && in.Body != nil {
		requests = in.Body.Requests
		quota = in.Body.Quota
	}
	report, err := h.svc.RunFlashSale(ctx, requests, quota)
	if err != nil {
		return nil, MapServiceError(ctx, h.logger, err)
	}
	return &struct{ Body *service.FlashSaleReport }{Body: report}, nil
}
