package service

import (
	"context"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/alertmanager/notify/webhook"
)

// loggingService is a logging middleware for Service.
type loggingService struct {
	logger log.Logger
	next   Service
}

// NewLoggingService creates a loggingService.
func NewLoggingService(logger log.Logger, next Service) Service {
	return loggingService{logger, next}
}

func (s loggingService) Post(ctx context.Context, wm webhook.Message) (pr PostResponse, err error) {
	defer func() {
		level.Debug(s.logger).Log(
			"response_message", pr.Message,
			"response_status", pr.Status,
			"webhook_url", pr.WebhookURL,
			"err", err,
		)
	}()
	return s.next.Post(ctx, wm)
}
