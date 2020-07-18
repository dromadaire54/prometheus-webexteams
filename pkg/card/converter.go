package card

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/prometheus/alertmanager/notify/webhook"
	"github.com/xeipuuv/gojsonschema"
)

// Converter converts an alert manager webhook message to Office365ConnectorCard.
type Converter interface {
	Convert(context.Context, webhook.Message) (string, error)
}

type loggingMiddleware struct {
	logger log.Logger
	next   Converter
}

var schema = loadSchema()

// NewCreatorLoggingMiddleware creates a loggingMiddleware.
func NewCreatorLoggingMiddleware(l log.Logger, n Converter) Converter {
	return loggingMiddleware{l, n}
}

func (l loggingMiddleware) Convert(ctx context.Context, a webhook.Message) (c string, err error) {
	defer func(begin time.Time) {
		documentLoader := gojsonschema.NewStringLoader(c)

		result, err := schema.Validate(documentLoader)
		if err != nil {
			l.logger.Log("erro´r", err)
		}

		//TODO: implement validation stuff
		if result.Valid() {
			l.logger.Log("alert", "The document is valid")
		} else {
			l.logger.Log("error", "The document is not valid. see errors :")
			for _, desc := range result.Errors() {
				l.logger.Log("error", desc)
			}
		}

		l.logger.Log(
			"alert", a,
			"validation", result.Errors(),
			"took", time.Since(begin),
		)
	}(time.Now())
	return l.next.Convert(ctx, a)
}

func loadSchema() *gojsonschema.Schema {
	schema, err := gojsonschema.NewSchema(gojsonschema.NewReferenceLoader("file://./resources/adaptive-card-schema.json"))
	if err != nil {
		fmt.Fprint(os.Stderr, err.Error())
		os.Exit(1)
	}
	return schema
}
