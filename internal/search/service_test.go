package search

import (
	"context"
	"errors"
	"testing"

	"github.com/lihongjie0209/microservice-platform-go/appaccess"
	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
)

type engineStub struct {
	visibility     []string
	searchRequest  *searchv1.SearchRequest
	suggestRequest *searchv1.SuggestRequest
	document       *searchv1.SearchDocument
}

type applicationVerifierStub struct {
	denied   string
	err      error
	verified []string
}

func (v *applicationVerifierStub) Check(_ context.Context, _ string, applicationIDs []string) (map[string]appaccess.Decision, error) {
	v.verified = append(v.verified, applicationIDs...)
	if v.err != nil {
		return nil, v.err
	}
	result := make(map[string]appaccess.Decision, len(applicationIDs))
	for _, applicationID := range applicationIDs {
		result[applicationID] = appaccess.Decision{Granted: applicationID != v.denied}
	}
	return result, nil
}

func TestSearchClassifiesApplicationDecisionOutageAsUnavailable(t *testing.T) {
	service, err := NewRuntime(&engineStub{}, &applicationVerifierStub{err: errors.New("upstream timeout")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Search(t.Context(), &searchv1.SearchRequest{TenantId: "tenant-1", ApplicationIds: []string{"app-1"}}, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func (*engineStub) Ping(context.Context) error { return nil }

func (e *engineStub) Search(_ context.Context, request *searchv1.SearchRequest, visibility []string) (*searchv1.SearchResponse, error) {
	e.visibility = visibility
	e.searchRequest = request
	return &searchv1.SearchResponse{Page: request.Page, PageSize: request.PageSize}, nil
}
func (e *engineStub) Suggest(_ context.Context, request *searchv1.SuggestRequest, _ []string) (*searchv1.SuggestResponse, error) {
	e.suggestRequest = request
	return &searchv1.SuggestResponse{}, nil
}
func (e *engineStub) Get(context.Context, string, string, []string) (*searchv1.SearchDocument, error) {
	if e.document != nil {
		return e.document, nil
	}
	return &searchv1.SearchDocument{}, nil
}

func TestSearchAuthorizesAndNormalizesApplicationFilters(t *testing.T) {
	engine := &engineStub{}
	verifier := &applicationVerifierStub{}
	service, err := NewRuntime(engine, verifier)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Search(t.Context(), &searchv1.SearchRequest{TenantId: "tenant-1", ApplicationIds: []string{" app-1 ", "app-1", "app-2"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier.verified) != 2 || len(engine.searchRequest.GetApplicationIds()) != 2 || engine.searchRequest.GetApplicationIds()[0] != "app-1" {
		t.Fatalf("verified=%v applications=%v", verifier.verified, engine.searchRequest.GetApplicationIds())
	}
}

func TestSuggestRejectsUngrantedApplicationBeforeEngine(t *testing.T) {
	engine := &engineStub{}
	service, err := NewRuntime(engine, &applicationVerifierStub{denied: "app-denied"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Suggest(t.Context(), &searchv1.SuggestRequest{TenantId: "tenant-1", Prefix: "invoice", ApplicationIds: []string{"app-denied"}}, nil)
	if !errors.Is(err, ErrForbidden) || engine.suggestRequest != nil {
		t.Fatalf("error=%v request=%+v", err, engine.suggestRequest)
	}
}

func TestGetAuthorizesPersistedDocumentApplication(t *testing.T) {
	engine := &engineStub{document: &searchv1.SearchDocument{TenantId: "tenant-1", ApplicationId: "app-1"}}
	verifier := &applicationVerifierStub{}
	service, err := NewRuntime(engine, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(t.Context(), "tenant-1", "document-1", nil); err != nil {
		t.Fatal(err)
	}
	if len(verifier.verified) != 1 || verifier.verified[0] != "app-1" {
		t.Fatalf("verified=%v", verifier.verified)
	}
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
