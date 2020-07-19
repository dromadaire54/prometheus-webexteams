FROM alpine:3.9.5

RUN apk --no-cache add ca-certificates tini

LABEL description="A lightweight Go Web Server that accepts POST alert message from Prometheus Alertmanager and sends it to Cisco Webex Teams Room."

COPY resources/default-message-card.tmpl resources/default-message-card.tmpl
COPY resources/webex-teams-request.tmpl resources/webex-teams-request.tmpl
COPY resources/adaptive-card-schema.json resources/adaptive-card-schema.json
COPY bin/prometheus-webexteams-linux-amd64 /promteams

ENTRYPOINT ["/sbin/tini", "--", "/promteams"]

EXPOSE 2000
