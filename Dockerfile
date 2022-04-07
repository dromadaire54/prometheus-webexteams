FROM golang:1.15 as builder
ARG VERSION

WORKDIR /app
COPY go.mod .
COPY go.sum .
RUN go mod download
COPY . ./
RUN go generate -v ./...
RUN CGO_ENABLED=0 GOOS=linux go build \
  -ldflags "-w -s \
    -X 'main.version=${VERSION}'" \
  -v -o prometheus-webexteams cmd/server/main.go

FROM alpine:3.9.5

ARG VERSION

RUN apk --update --no-cache add \
    ca-certificates tini \
  && addgroup promteams \
  && adduser -D -G promteams -s /bin/sh promteams \
  && rm -rf /tmp/* /var/cache/apk/*

COPY --from=builder /app/prometheus-webexteams /promteams

LABEL description="A lightweight Go Web Server that accepts POST alert message from Prometheus Alertmanager and sends it to Cisco Webex Teams Room."

COPY resources/default-message-card.tmpl resources/default-message-card.tmpl
COPY resources/webex-teams-request.tmpl resources/webex-teams-request.tmpl
COPY resources/adaptive-card-schema.json resources/adaptive-card-schema.json

ENTRYPOINT ["/sbin/tini", "--", "/promteams"]


EXPOSE 2000
