package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/infonova/prometheus-webexteams/pkg/card"
	"github.com/infonova/prometheus-webexteams/pkg/service"
	"github.com/infonova/prometheus-webexteams/pkg/transport"
	"github.com/infonova/prometheus-webexteams/pkg/version"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	ocprometheus "contrib.go.opencensus.io/exporter/prometheus"
	"github.com/labstack/echo/v4"
	stdprometheus "github.com/prometheus/client_golang/prometheus"

	"contrib.go.opencensus.io/exporter/jaeger"
	"go.opencensus.io/plugin/ochttp"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"go.opencensus.io/trace"

	_ "net/http/pprof" //nolint: gosec

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/oklog/run"
	"github.com/peterbourgon/ff"
	"gopkg.in/yaml.v2"
)

// PromTeamsConfig is the struct representation of the config file.
type PromTeamsConfig struct {
	Connectors []Connector `yaml:"connectors"`
}

// ConnectorWithCustomTemplate .
type Connector struct {
	RequestPath       string `yaml:"request_path"`
	AccessToken       string `yaml:"access_token"`
	RoomId            string `yaml:"room_id"`
	TemplateFile      string `yaml:"template_file"`
	WebhookURL        string `yaml:"webhook_url"`
	EscapeUnderscores bool   `yaml:"escape_underscores"`
}

func parseTeamsConfigFile(f string) (PromTeamsConfig, error) {
	b, err := ioutil.ReadFile(f)
	if err != nil {
		return PromTeamsConfig{}, err
	}
	var tc PromTeamsConfig
	if err = yaml.Unmarshal(b, &tc); err != nil {
		return PromTeamsConfig{}, err
	}
	return tc, nil
}

func main() { //nolint: funlen
	var (
		fs                            = flag.NewFlagSet("prometheus-webexteams", flag.ExitOnError)
		promVersion                   = fs.Bool("version", false, "Print the version")
		logFormat                     = fs.String("log-format", "json", "json|fmt")
		debugLogs                     = fs.Bool("debug", true, "Set log level to debug mode.")
		jaegerTrace                   = fs.Bool("jaeger-trace", false, "Send traces to Jaeger.")
		jaegerAgentAddr               = fs.String("jaeger-agent", "localhost:6831", "Jaeger agent endpoint")
		httpAddr                      = fs.String("http-addr", ":2000", "HTTP listen address.")
		requestURI                    = fs.String("request-uri", "alertmanager", "The default request URI path where Prometheus will post to.")
		teamsWebhookURL               = fs.String("teams-webhook-url", "https://webexapis.com/v1/messages", "The default Webex Teams webhook connector.")
		teamsAccessToken              = fs.String("teams-access-token", "", "The access token to authorize the requests.")
		teamsRoomId                   = fs.String("teams-room-id", "", "The room specifies the target room of the messages.")
		templateFile                  = fs.String("template-file", "resources/default-message-card.tmpl", "The default Webex Teams Message Card template file.")
		escapeUnderscores             = fs.Bool("escape-underscores", false, "Automatically replace all '_' with '\\_' from texts in the alert.")
		configFile                    = fs.String("config-file", "", "The connectors configuration file.")
		httpClientIdleConnTimeout     = fs.Duration("idle-conn-timeout", 90*time.Second, "The HTTP client idle connection timeout duration.")
		httpClientTLSHandshakeTimeout = fs.Duration("tls-handshake-timeout", 30*time.Second, "The HTTP client TLS handshake timeout.")
		httpClientMaxIdleConn         = fs.Int("max-idle-conns", 100, "The HTTP client maximum number of idle connections")
	)

	if err := ff.Parse(fs, os.Args[1:], ff.WithEnvVarNoPrefix()); err != nil {
		fmt.Fprint(os.Stderr, err.Error())
		os.Exit(1)
	}

	if *promVersion {
		fmt.Println(version.VERSION)
		os.Exit(0)
	}

	// Logger.
	var logger log.Logger
	{
		switch *logFormat {
		case "json":
			logger = log.NewJSONLogger(log.NewSyncWriter(os.Stdout))
		case "fmt":
			logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
		default:
			fmt.Fprintf(os.Stderr, "log-format %s is not valid", *logFormat)
			os.Exit(1)
		}
		if *debugLogs {
			logger = level.NewFilter(logger, level.AllowDebug())
		} else {
			logger = level.NewFilter(logger, level.AllowInfo())
		}
		logger = log.With(logger, "ts", log.DefaultTimestamp, "caller", log.DefaultCaller)
	}

	// Tracer.
	if *jaegerTrace {
		logger.Log("message", "jaeger tracing enabled")

		je, err := jaeger.NewExporter(
			jaeger.Options{
				AgentEndpoint: *jaegerAgentAddr,
				ServiceName:   "prometheus-webexteams",
			},
		)
		if err != nil {
			fmt.Fprint(os.Stderr, err.Error())
			os.Exit(1)
		}

		trace.RegisterExporter(je)
		trace.ApplyConfig(
			trace.Config{
				DefaultSampler: trace.AlwaysSample(),
			},
		)
	}

	// Prepare the Teams config.
	var (
		tc  PromTeamsConfig
		err error
	)

	// Parse the config file if defined.
	if *configFile != "" {
		// parse config file
		tc, err = parseTeamsConfigFile(*configFile)
		if err != nil {
			fmt.Fprint(os.Stderr, err.Error())
			os.Exit(1)
		}
	} else {
		// create a connector from flags
		tc.Connectors = append(
			tc.Connectors,
			Connector{
				RequestPath:       *requestURI,
				WebhookURL:        *teamsWebhookURL,
				AccessToken:       *teamsAccessToken,
				RoomId:            *teamsRoomId,
				TemplateFile:      *templateFile,
				EscapeUnderscores: *escapeUnderscores,
			},
		)
	}

	// Templated card defaultConverter setup.
	var defaultConverter card.Converter
	{
		tmpl, err := card.ParseTemplateFile(*templateFile)
		if err != nil {
			logger.Log("err", err)
		}
		defaultConverter = card.NewTemplatedCardCreator(tmpl, *escapeUnderscores)
		defaultConverter = card.NewCreatorLoggingMiddleware(
			log.With(
				logger,
				"template_file", *templateFile,
				"escaped_underscores", *escapeUnderscores,
			),
			defaultConverter,
		)
	}

	// Teams HTTP client setup.
	httpClient := &http.Client{
		Transport: &ochttp.Transport{
			Base: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:          *httpClientMaxIdleConn,
				IdleConnTimeout:       *httpClientIdleConnTimeout,
				TLSHandshakeTimeout:   *httpClientTLSHandshakeTimeout,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}

	var routes []transport.Route
	for _, c := range tc.Connectors {

		// check connector configuration
		if len(c.RequestPath) == 0 {
			logger.Log("err", "one of the 'templated_connectors' is missing a 'request_path'")
			os.Exit(1)
		}
		if len(c.WebhookURL) == 0 {
			logger.Log("err", fmt.Sprintf("The teams-webhook-url is required for request_path '%s'", c.RequestPath))
			os.Exit(1)
		}
		if len(c.AccessToken) == 0 {
			logger.Log("err", fmt.Sprintf("The teams-access-token is required for request_path '%s'", c.RequestPath))
			os.Exit(1)
		}
		if len(c.RoomId) == 0 {
			logger.Log("err", fmt.Sprintf("The teams-room-id is required for request_path '%s'", c.RequestPath))
			os.Exit(1)
		}
		if len(c.TemplateFile) == 0 {
			logger.Log("err", fmt.Sprintf("The template_file is required for request_path '%s'", c.RequestPath))
			os.Exit(1)
		}

		var converter card.Converter
		tmpl, err := card.ParseTemplateFile(c.TemplateFile)
		if err != nil {
			logger.Log("err", err)
			os.Exit(1)
		}

		converter = card.NewTemplatedCardCreator(tmpl, c.EscapeUnderscores)
		converter = card.NewCreatorLoggingMiddleware(
			log.With(
				logger,
				"template_file", c.TemplateFile,
				"escaped_underscores", c.EscapeUnderscores,
			),
			converter,
		)

		var r transport.Route
		r.RequestPath = c.RequestPath
		r.Service = service.NewSimpleService(converter, httpClient, c.WebhookURL, c.AccessToken, c.RoomId)
		r.Service = service.NewLoggingService(logger, r.Service)
		routes = append(routes, r)
	}

	if err := checkDuplicateRequestPath(routes); err != nil {
		logger.Log("err", err)
		os.Exit(1)
	}

	pe, err := ocprometheus.NewExporter(
		ocprometheus.Options{
			Registry: stdprometheus.DefaultRegisterer.(*stdprometheus.Registry),
		},
	)
	if err != nil {
		logger.Log("err", err)
		os.Exit(1)
	}
	if err := view.Register(ocviews()...); err != nil {
		logger.Log("err", err)
		os.Exit(1)
	}

	// Prometheus webex teams HTTP handler setup.
	var handler *echo.Echo
	{
		// Main app.
		handler = transport.NewServer(logger, routes...)
		// Prometheus metrics.
		handler.GET("/metrics", echo.WrapHandler(pe))
		// Pprof.
		handler.GET("/debug/pprof/*", echo.WrapHandler(http.DefaultServeMux))
		// Config.
		handler.GET("/config", func(c echo.Context) error {
			return c.JSON(200, tc.Connectors)
		})
	}

	var g run.Group
	{
		srv := http.Server{
			Addr:    *httpAddr,
			Handler: handler,
		}
		g.Add(
			func() error {
				logger.Log(
					"listen_http_addr", *httpAddr,
					"version", version.VERSION,
					"commit", version.COMMIT,
					"branch", version.BRANCH,
					"build_date", version.BUILDDATE,
				)
				return srv.ListenAndServe()
			},
			func(error) {
				if err != http.ErrServerClosed {
					if err := srv.Shutdown(context.Background()); err != nil {
						logger.Log("err", err)
					}
				}
			},
		)
	}
	{
		g.Add(run.SignalHandler(context.Background(), syscall.SIGINT, syscall.SIGTERM))
	}
	logger.Log("exit", g.Run())
}

func ocviews() []*view.View {
	clientKeys := []tag.Key{
		ochttp.KeyClientMethod, ochttp.KeyClientStatus, ochttp.KeyClientHost, ochttp.KeyClientPath,
	}
	serverKeys := []tag.Key{
		ochttp.StatusCode, ochttp.Method, ochttp.Path,
	}
	return []*view.View{
		// HTTP client metrics.
		{
			Name:        "http/client/sent_bytes",
			Measure:     ochttp.ClientSentBytes,
			Aggregation: view.Distribution(1024, 2048, 4096, 16384, 65536, 262144, 1048576, 4194304),
			Description: "Total bytes sent in request body (not including headers), by HTTP method and response status",
			TagKeys:     clientKeys,
		},
		{
			Name:        "http/client/received_bytes",
			Measure:     ochttp.ClientReceivedBytes,
			Aggregation: view.Distribution(1024, 2048, 4096, 16384, 65536, 262144, 1048576, 4194304),
			Description: "Total bytes received in response bodies (not including headers but including error responses with bodies), by HTTP method and response status",
			TagKeys:     clientKeys,
		},
		{
			Name:        "http/client/roundtrip_latency",
			Measure:     ochttp.ClientRoundtripLatency,
			Aggregation: view.Distribution(1, 2, 3, 4, 5, 6, 8, 10, 13, 16, 20, 25, 30),
			Description: "End-to-end latency, by HTTP method and response status",
			TagKeys:     clientKeys,
		},
		{
			Name:        "http/client/completed_count",
			Measure:     ochttp.ClientRoundtripLatency,
			Aggregation: view.Count(),
			Description: "Count of completed requests, by HTTP method and response status",
			TagKeys:     clientKeys,
		},
		// HTTP server metrics.
		{
			Name:        "http/server/request_count",
			Description: "Count of HTTP requests started",
			Measure:     ochttp.ServerRequestCount,
			Aggregation: view.Count(),
			TagKeys:     serverKeys,
		},
		{
			Name:        "http/server/request_bytes",
			Description: "Size distribution of HTTP request body",
			Measure:     ochttp.ServerRequestBytes,
			Aggregation: view.Distribution(1024, 2048, 4096, 16384, 65536, 262144, 1048576, 4194304),
			TagKeys:     serverKeys,
		},
		{
			Name:        "http/server/response_bytes",
			Description: "Size distribution of HTTP response body",
			Measure:     ochttp.ServerResponseBytes,
			Aggregation: view.Distribution(1024, 2048, 4096, 16384, 65536, 262144, 1048576, 4194304),
			TagKeys:     serverKeys,
		},
		{
			Name:        "http/server/latency",
			Description: "Latency distribution of HTTP requests",
			Measure:     ochttp.ServerLatency,
			Aggregation: view.Distribution(1, 2, 3, 4, 5, 6, 8, 10, 13, 16, 20, 25, 30),
			TagKeys:     serverKeys,
		},
	}
}

func checkDuplicateRequestPath(routes []transport.Route) error {
	added := map[string]bool{}
	for _, r := range routes {
		if _, ok := added[r.RequestPath]; ok {
			return fmt.Errorf("found duplicate use of request path '%s'", r.RequestPath)
		}
		added[r.RequestPath] = true
	}
	return nil
}
