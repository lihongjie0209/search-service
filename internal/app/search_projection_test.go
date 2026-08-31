package app

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	platforminbox "github.com/lihongjie0209/microservice-platform-go/inbox"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
	"github.com/lihongjie0209/search-service/internal/config"
	searchapp "github.com/lihongjie0209/search-service/internal/search"
	"google.golang.org/protobuf/proto"
)

type projectionEngine struct {
	upserts int
	deletes int
}

func (*projectionEngine) Ping(context.Context) error { return nil }
func (*projectionEngine) Search(context.Context, *searchv1.SearchRequest, []string) (*searchv1.SearchResponse, error) {
	return nil, nil
}
func (*projectionEngine) Suggest(context.Context, *searchv1.SuggestRequest, []string) (*searchv1.SuggestResponse, error) {
	return nil, nil
}
func (*projectionEngine) Get(context.Context, string, string, []string) (*searchv1.SearchDocument, error) {
	return nil, nil
}
func (e *projectionEngine) Upsert(context.Context, []*searchv1.SearchDocument) error {
	e.upserts++
	return nil
}
func (e *projectionEngine) Delete(context.Context, []*searchv1.DocumentKey) error {
	e.deletes++
	return nil
}

func TestApplySearchEvent(t *testing.T) {
	engine := &projectionEngine{}
	service := searchapp.New(engine)
	document := &searchv1.SearchDocument{Id: "t:s:type:1", TenantId: "t", SourceService: "s", DocumentType: "type", SourceId: "1", SourceVersion: 1, VisibilityTokens: []string{"tenant:t:*"}}
	payload, err := proto.Marshal(&searchv1.SearchDocumentUpsertedEvent{Document: document})
	if err != nil {
		t.Fatal(err)
	}
	err = applySearchEvent(t.Context(), service, &commonv1.EventEnvelope{EventType: searchUpsertedEvent, TenantId: "t", SchemaVersion: 1, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if engine.upserts != 1 {
		t.Fatalf("upserts = %d", engine.upserts)
	}
}

func TestApplySearchEventRejectsTenantMismatch(t *testing.T) {
	engine := &projectionEngine{}
	service := searchapp.New(engine)
	payload, _ := proto.Marshal(&searchv1.SearchDocumentDeletedEvent{Document: &searchv1.DocumentKey{TenantId: "other"}})
	if err := applySearchEvent(t.Context(), service, &commonv1.EventEnvelope{EventType: searchDeletedEvent, TenantId: "t", Payload: payload}); err == nil {
		t.Fatal("expected tenant mismatch")
	}
}

func TestNewSearchInboxRetentionCleaner_OnlyRunsForEnabledEventBus(t *testing.T) {
	inbox, err := platforminbox.NewSQLStore(sqlx.NewDb(&sql.DB{}, "pgx"), platforminbox.DialectPostgres, "search_event_inbox")
	if err != nil {
		t.Fatal(err)
	}

	cleaner, err := newSearchInboxRetentionCleaner(inbox, config.Config{})
	if err != nil || cleaner != nil {
		t.Fatalf("disabled event bus cleaner = (%v, %v), want (nil, nil)", cleaner, err)
	}
	cleaner, err = newSearchInboxRetentionCleaner(inbox, config.Config{EventBus: config.EventBus{Enabled: true, InboxRetention: 14 * 24 * time.Hour, InboxCleanupBatchSize: 500}})
	if err != nil || cleaner == nil {
		t.Fatalf("enabled event bus cleaner = (%v, %v), want non-nil cleaner", cleaner, err)
	}
}
