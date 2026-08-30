package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jmoiron/sqlx"
	platforminbox "github.com/lihongjie0209/microservice-platform-go/inbox"
	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
	"github.com/lihongjie0209/search-service/internal/config"
	"github.com/lihongjie0209/search-service/internal/eventbus"
	searchapp "github.com/lihongjie0209/search-service/internal/search"
	"go.uber.org/fx"
	"google.golang.org/protobuf/proto"
)

const (
	searchUpsertedEvent = "platform.search.document.upserted.v1"
	searchDeletedEvent  = "platform.search.document.deleted.v1"
)

func newSearchInbox(db *sqlx.DB, cfg config.Config) (*platforminbox.SQLStore, error) {
	if db == nil {
		return nil, nil
	}
	dialect := platforminbox.DialectPostgres
	if cfg.Database.Type == "mysql" {
		dialect = platforminbox.DialectMySQL
	}
	if cfg.Database.Type == "kingbase" {
		dialect = platforminbox.DialectKingbase
	}
	table := "search_event_inbox"
	if cfg.Database.Schema != "" && cfg.Database.Type != "mysql" {
		table = cfg.Database.Schema + "." + table
	}
	return platforminbox.NewSQLStore(db, dialect, table)
}

func startSearchProjection(lifecycle fx.Lifecycle, cfg config.Config, bus *eventbus.Bus, inbox *platforminbox.SQLStore, service *searchapp.Service, logger *slog.Logger) {
	var cancel context.CancelFunc
	var worker sync.WaitGroup
	lifecycle.Append(fx.Hook{OnStart: func(context.Context) error {
		if bus == nil {
			return nil
		}
		if inbox == nil {
			return errors.New("search projection requires Inbox database")
		}
		runCtx, stop := context.WithCancel(context.Background())
		cancel = stop
		worker.Add(1)
		go func() {
			defer worker.Done()
			err := bus.ConsumeWithOptions(runCtx, eventbus.ConsumerOptions{Durable: cfg.Projection.Durable, FilterSubject: cfg.Projection.Subject, Handler: func(ctx context.Context, envelope *eventbus.Envelope) error {
				if envelope.GetSchemaVersion() != 1 {
					return fmt.Errorf("unsupported search event schema version %d", envelope.GetSchemaVersion())
				}
				_, err := inbox.Process(ctx, platforminbox.Key{Consumer: cfg.Projection.Durable, EventID: envelope.GetEventId()}, "search-event-consumer", func(ctx context.Context, _ *sqlx.Tx) error { return applySearchEvent(ctx, service, envelope) })
				return err
			}, OnError: func(err error) { logger.Error("search projection event", "error", err) }})
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("search projection stopped", "error", err)
			}
		}()
		return nil
	}, OnStop: func(context.Context) error {
		if cancel != nil {
			cancel()
		}
		worker.Wait()
		return nil
	}})
}

func applySearchEvent(ctx context.Context, service *searchapp.Service, envelope *eventbus.Envelope) error {
	switch envelope.GetEventType() {
	case searchUpsertedEvent:
		payload := new(searchv1.SearchDocumentUpsertedEvent)
		if err := proto.Unmarshal(envelope.GetPayload(), payload); err != nil {
			return fmt.Errorf("decode upsert event: %w", err)
		}
		if payload.GetDocument() == nil || envelope.GetTenantId() != payload.GetDocument().GetTenantId() {
			return errors.New("search upsert tenant mismatch")
		}
		return service.BatchUpsert(ctx, []*searchv1.SearchDocument{payload.GetDocument()})
	case searchDeletedEvent:
		payload := new(searchv1.SearchDocumentDeletedEvent)
		if err := proto.Unmarshal(envelope.GetPayload(), payload); err != nil {
			return fmt.Errorf("decode delete event: %w", err)
		}
		if payload.GetDocument() == nil || envelope.GetTenantId() != payload.GetDocument().GetTenantId() {
			return errors.New("search delete tenant mismatch")
		}
		return service.BatchDelete(ctx, []*searchv1.DocumentKey{payload.GetDocument()})
	default:
		return fmt.Errorf("unsupported search event type %q", envelope.GetEventType())
	}
}

var SearchProjectionModule = fx.Module("search-projection", fx.Provide(newSearchInbox), fx.Invoke(startSearchProjection))
