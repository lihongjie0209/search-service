package search

import (
	"context"
	"errors"
	"testing"

	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
)

type engineStub struct{ visibility []string }

func (*engineStub) Ping(context.Context) error { return nil }

func (e *engineStub) Search(_ context.Context, request *searchv1.SearchRequest, visibility []string) (*searchv1.SearchResponse, error) {
	e.visibility = visibility
	return &searchv1.SearchResponse{Page: request.Page, PageSize: request.PageSize}, nil
}
func (*engineStub) Suggest(context.Context, *searchv1.SuggestRequest, []string) (*searchv1.SuggestResponse, error) {
	return &searchv1.SuggestResponse{}, nil
}
func (*engineStub) Get(context.Context, string, string, []string) (*searchv1.SearchDocument, error) {
	return &searchv1.SearchDocument{}, nil
}
func (*engineStub) Upsert(context.Context, []*searchv1.SearchDocument) error { return nil }
func (*engineStub) Delete(context.Context, []*searchv1.DocumentKey) error    { return nil }

func TestSearchNormalizesPaginationAndVisibility(t *testing.T) {
	engine := &engineStub{}
	response, err := New(engine).Search(context.Background(), &searchv1.SearchRequest{TenantId: "tenant-1", PageSize: 999}, []string{"user:u1", "user:u1"})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetPage() != 1 || response.GetPageSize() != 100 {
		t.Fatalf("pagination = %d/%d", response.GetPage(), response.GetPageSize())
	}
	if len(engine.visibility) != 2 || engine.visibility[0] != "tenant:tenant-1:*" || engine.visibility[1] != "user:u1" {
		t.Fatalf("visibility = %v", engine.visibility)
	}
}

func TestBatchUpsertRejectsIncompleteDocument(t *testing.T) {
	err := New(&engineStub{}).BatchUpsert(context.Background(), []*searchv1.SearchDocument{{Id: "id"}})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v", err)
	}
}

func TestBatchUpsertDerivesCanonicalID(t *testing.T) {
	engine := &captureEngine{}
	document := &searchv1.SearchDocument{Id: "caller-controlled", TenantId: "tenant", SourceService: "apps", DocumentType: "application", SourceId: "app-1", SourceVersion: 1, VisibilityTokens: []string{"tenant:tenant:*"}}
	if err := New(engine).BatchUpsert(t.Context(), []*searchv1.SearchDocument{document}); err != nil {
		t.Fatal(err)
	}
	if document.GetId() != "tenant:apps:application:app-1" {
		t.Fatalf("id = %q", document.GetId())
	}
}

func TestBatchUpsertRejectsBroadVisibilityForProtectedDocument(t *testing.T) {
	document := &searchv1.SearchDocument{TenantId: "tenant", SourceService: "apps", DocumentType: "application", SourceId: "app-1", SourceVersion: 1, PermissionCode: "application.read", VisibilityTokens: []string{"tenant:tenant:*"}}
	if err := New(&engineStub{}).BatchUpsert(t.Context(), []*searchv1.SearchDocument{document}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("error = %v", err)
	}
}

type captureEngine struct{ engineStub }
