package service

import (
	"context"
	"fmt"
	"github.com/infonova/prometheus-webexteams/pkg/card"
	"io/ioutil"
	"net/http"
	"strings"

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
	webhookURL  string
	accessToken string
	roomId      string
}

type requestData struct {
	RoomId string
	Card   string
}

// NewSimpleService creates a simpleService.
func NewSimpleService(converter card.Converter, client *http.Client, webhookURL string, accessToken string, roomId string) Service {
	return simpleService{converter, client, webhookURL, accessToken, roomId}
}

func (s simpleService) Post(ctx context.Context, wm webhook.Message) (PostResponse, error) {
	ctx, span := trace.StartSpan(ctx, "simpleService.Post")
	defer span.End()

	c, err := s.converter.Convert(ctx, wm)
	if err != nil {
		return PostResponse{}, fmt.Errorf("failed to parse webhook message: %w", err)
	}

	pr, err := s.post(ctx, c, s.webhookURL)

	return pr, nil
}

func (s simpleService) post(ctx context.Context, c string, url string) (PostResponse, error) {
	ctx, span := trace.StartSpan(ctx, "simpleService.post")
	defer span.End()

	pr := PostResponse{WebhookURL: url}

	data := requestData{
		RoomId: s.roomId,
		Card:   c,
	}
	tmpl, err := card.ParseTemplateFile("./resources/webex-teams-request.tmpl")
	if err != nil {
		err = fmt.Errorf("load 'message.request' template failed: %w", err)
		return pr, err
	}

	var reqStr string
	reqStr, err = tmpl.ExecuteTextString(`{{ template "teams.request" . }}`, data)
	if err != nil {
		err = fmt.Errorf("execute 'message.request' template failed: %w", err)
		return pr, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.webhookURL, strings.NewReader(reqStr))
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
