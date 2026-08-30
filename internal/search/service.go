package search

import (
	"context"
	"errors"
	"fmt"
	"strings"

	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
	"go.uber.org/fx"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	maxBatchSize    = 500
)

var (
	ErrInvalidArgument = errors.New("invalid search argument")
	ErrNotFound        = errors.New("search document not found")
	ErrUnavailable     = errors.New("search engine unavailable")
)

// Engine is the projection store boundary. Implementations must apply source_version
// monotonically so delayed events cannot overwrite newer documents.
type Engine interface {
	Ping(context.Context) error
	Search(context.Context, *searchv1.SearchRequest, []string) (*searchv1.SearchResponse, error)
	Suggest(context.Context, *searchv1.SuggestRequest, []string) (*searchv1.SuggestResponse, error)
	Get(context.Context, string, string, []string) (*searchv1.SearchDocument, error)
	Upsert(context.Context, []*searchv1.SearchDocument) error
	Delete(context.Context, []*searchv1.DocumentKey) error
}

type Service struct{ engine Engine }

func New(engine Engine) *Service { return &Service{engine: engine} }

func (s *Service) Ping(ctx context.Context) error { return s.engine.Ping(ctx) }

func (s *Service) Search(ctx context.Context, request *searchv1.SearchRequest, visibility []string) (*searchv1.SearchResponse, error) {
	if request == nil || strings.TrimSpace(request.GetTenantId()) == "" {
		return nil, fmt.Errorf("%w: tenant_id is required", ErrInvalidArgument)
	}
	normalizePage(request)
	return s.engine.Search(ctx, request, normalizeVisibility(request.GetTenantId(), visibility))
}

func (s *Service) Suggest(ctx context.Context, request *searchv1.SuggestRequest, visibility []string) (*searchv1.SuggestResponse, error) {
	if request == nil || strings.TrimSpace(request.GetTenantId()) == "" || strings.TrimSpace(request.GetPrefix()) == "" {
		return nil, fmt.Errorf("%w: tenant_id and prefix are required", ErrInvalidArgument)
	}
	if request.Limit == 0 {
		request.Limit = 10
	}
	if request.Limit > 50 {
		request.Limit = 50
	}
	return s.engine.Suggest(ctx, request, normalizeVisibility(request.GetTenantId(), visibility))
}

func (s *Service) Get(ctx context.Context, tenantID, id string, visibility []string) (*searchv1.SearchDocument, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: tenant_id and id are required", ErrInvalidArgument)
	}
	return s.engine.Get(ctx, tenantID, id, normalizeVisibility(tenantID, visibility))
}

func (s *Service) BatchUpsert(ctx context.Context, documents []*searchv1.SearchDocument) error {
	if len(documents) == 0 || len(documents) > maxBatchSize {
		return fmt.Errorf("%w: documents must contain 1..%d items", ErrInvalidArgument, maxBatchSize)
	}
	for _, document := range documents {
		if document == nil || document.GetTenantId() == "" || document.GetSourceService() == "" || document.GetDocumentType() == "" || document.GetSourceId() == "" || document.GetSourceVersion() <= 0 || len(document.GetVisibilityTokens()) == 0 {
			return fmt.Errorf("%w: incomplete document", ErrInvalidArgument)
		}
		if err := validateVisibility(document); err != nil {
			return err
		}
		document.Id = CanonicalID(document.GetTenantId(), document.GetSourceService(), document.GetDocumentType(), document.GetSourceId())
	}
	return s.engine.Upsert(ctx, documents)
}

func validateVisibility(document *searchv1.SearchDocument) error {
	broad := "tenant:" + document.GetTenantId() + ":*"
	for _, token := range document.GetVisibilityTokens() {
		token = strings.TrimSpace(token)
		valid := token == broad
		for _, prefix := range []string{"user:", "membership:", "role:", "service-account:"} {
			valid = valid || strings.HasPrefix(token, prefix) && len(token) > len(prefix)
		}
		if !valid {
			return fmt.Errorf("%w: invalid visibility token", ErrInvalidArgument)
		}
		if document.GetPermissionCode() != "" && token == broad {
			return fmt.Errorf("%w: permission-protected document cannot use tenant-wide visibility", ErrInvalidArgument)
		}
	}
	return nil
}

func CanonicalID(tenantID, sourceService, documentType, sourceID string) string {
	return strings.Join([]string{tenantID, sourceService, documentType, sourceID}, ":")
}

func (s *Service) BatchDelete(ctx context.Context, documents []*searchv1.DocumentKey) error {
	if len(documents) == 0 || len(documents) > maxBatchSize {
		return fmt.Errorf("%w: documents must contain 1..%d items", ErrInvalidArgument, maxBatchSize)
	}
	for _, document := range documents {
		if document == nil || document.GetTenantId() == "" || document.GetSourceService() == "" || document.GetDocumentType() == "" || document.GetSourceId() == "" || document.GetSourceVersion() <= 0 {
			return fmt.Errorf("%w: incomplete document key", ErrInvalidArgument)
		}
	}
	return s.engine.Delete(ctx, documents)
}

func normalizePage(request *searchv1.SearchRequest) {
	if request.Page == 0 {
		request.Page = 1
	}
	if request.PageSize == 0 {
		request.PageSize = defaultPageSize
	}
	if request.PageSize > maxPageSize {
		request.PageSize = maxPageSize
	}
}

func normalizeVisibility(tenantID string, values []string) []string {
	result := []string{"tenant:" + tenantID + ":*"}
	seen := map[string]struct{}{result[0]: {}}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

var Module = fx.Module("search", fx.Provide(New))
