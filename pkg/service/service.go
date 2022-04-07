package service

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	"github.com/infonova/prometheus-webexteams/pkg/card"

	"github.com/prometheus/alertmanager/notify/webhook"
	"go.opencensus.io/trace"
)

// PostResponse is the prometheus webex teams service response.
type PostResponse struct {
	WebhookURL string `json:"webhook_url"`
	Status     int    `json:"status"`
	Message    string `json:"message"`
}

// Service is the Alertmanager to Webex Teams webhook service.
type Service interface {
	Post(context.Context, webhook.Message) (resp PostResponse, err error)
}

type simpleService struct {
	converter   card.Converter
	client      *http.Client
	template    string
	webhookURL  string
	accessToken string
	roomID      string
}

type requestData struct {
	RoomID string
	Card   string
}

// NewSimpleService creates a simpleService.
func NewSimpleService(converter card.Converter, client *http.Client, template string, webhookURL string, accessToken string, roomID string) Service {
	return simpleService{converter, client, template, webhookURL, accessToken, roomID}
}

func (s simpleService) Post(ctx context.Context, wm webhook.Message) (PostResponse, error) {
	ctx, span := trace.StartSpan(ctx, "simpleService.Post")
	defer span.End()

	c, err := s.converter.Convert(ctx, wm)
	if err != nil {
		return PostResponse{}, fmt.Errorf("failed to parse webhook message: %w", err)
	}

	pr, err := s.post(ctx, c, s.webhookURL)

	if err != nil {
		return pr, err
	}

	return pr, nil
}

func (s simpleService) post(ctx context.Context, c string, url string) (PostResponse, error) {
	ctx, span := trace.StartSpan(ctx, "simpleService.post")
	defer span.End()

	pr := PostResponse{WebhookURL: url}
	req, err := http.NewRequestWithContext(ctx, "POST", s.webhookURL, strings.NewReader(c))
	if err != nil {
		err = fmt.Errorf("new request with context failed: %w", err)
		return pr, err
	}

	// add authorization header to the request
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", s.accessToken))
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		err = fmt.Errorf("http client failed: %w", err)
		return pr, err
	}
	defer resp.Body.Close()

	pr.Status = resp.StatusCode

	rb, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		err = fmt.Errorf("failed reading http response body: %w", err)
		pr.Message = err.Error()
		return pr, err
	}
	pr.Message = string(rb)

	return pr, nil
}
