package httptransport

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
	"github.com/lihongjie0209/search-service/internal/apperror"
	searchapp "github.com/lihongjie0209/search-service/internal/search"
	"github.com/lihongjie0209/search-service/internal/searchauth"
)

type SearchRequest struct {
	TenantID       string              `json:"tenant_id" binding:"required"`
	Query          string              `json:"query"`
	DocumentTypes  []string            `json:"document_types"`
	ApplicationIDs []string            `json:"application_ids"`
	Filters        map[string][]string `json:"filters"`
	Sort           string              `json:"sort"`
	Page           uint32              `json:"page"`
	PageSize       uint32              `json:"page_size"`
	FacetFields    []string            `json:"facet_fields"`
}

type SuggestRequest struct {
	TenantID      string   `json:"tenant_id" binding:"required"`
	Prefix        string   `json:"prefix" binding:"required"`
	DocumentTypes []string `json:"document_types"`
	Limit         uint32   `json:"limit"`
}
type GetSearchDocumentRequest struct {
	TenantID string `json:"tenant_id" binding:"required"`
	ID       string `json:"id" binding:"required"`
}

type SearchDocumentDTO struct {
	ID              string            `json:"id"`
	TenantID        string            `json:"tenant_id"`
	SourceService   string            `json:"source_service"`
	DocumentType    string            `json:"document_type"`
	SourceID        string            `json:"source_id"`
	ApplicationID   string            `json:"application_id"`
	Title           string            `json:"title"`
	Summary         string            `json:"summary"`
	Content         string            `json:"content"`
	URL             string            `json:"url"`
	Icon            string            `json:"icon"`
	Keywords        []string          `json:"keywords"`
	Attributes      map[string]string `json:"attributes"`
	PermissionCode  string            `json:"permission_code"`
	SourceVersion   int64             `json:"source_version"`
	SourceCreatedAt *time.Time        `json:"source_created_at,omitempty"`
	SourceUpdatedAt *time.Time        `json:"source_updated_at,omitempty"`
}
type HighlightDTO struct {
	Field     string   `json:"field"`
	Fragments []string `json:"fragments"`
}
type SearchHitDTO struct {
	Document   SearchDocumentDTO `json:"document"`
	Score      float64           `json:"score"`
	Highlights []HighlightDTO    `json:"highlights"`
}
type FacetBucketDTO struct {
	Value string `json:"value"`
	Count uint64 `json:"count"`
}
type FacetDTO struct {
	Field   string           `json:"field"`
	Buckets []FacetBucketDTO `json:"buckets"`
}
type SearchPageDTO struct {
	Items            []SearchHitDTO `json:"items"`
	Total            uint64         `json:"total"`
	Page             uint32         `json:"page"`
	PageSize         uint32         `json:"page_size"`
	Facets           []FacetDTO     `json:"facets"`
	TookMilliseconds uint64         `json:"took_milliseconds"`
}
type SuggestionDTO struct {
	Text         string `json:"text"`
	DocumentType string `json:"document_type"`
	SourceID     string `json:"source_id"`
	URL          string `json:"url"`
}
type SuggestResponseDTO struct {
	Items []SuggestionDTO `json:"items"`
}

// Search godoc
// @Summary Search tenant-visible documents
// @Tags search
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body SearchRequest true "Search query"
// @Success 200 {object} Response{body=SearchPageDTO}
// @Failure 400 {object} Response "Code 10001: invalid request"
// @Failure 401 {object} Response "Code 20001: unauthorized"
// @Failure 503 {object} Response "Code 50003: search unavailable"
// @Router /api/v1/search/query [post]
func (h *Handler) Search(c *gin.Context) {
	var request SearchRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid search request", err))
		return
	}
	filters := make(map[string]*searchv1.StringValues, len(request.Filters))
	for field, values := range request.Filters {
		filters[field] = &searchv1.StringValues{Values: values}
	}
	visibility, scopeErr := h.visibility(c, request.TenantID)
	if scopeErr != nil {
		Fail(c, h.logger, scopeErr)
		return
	}
	response, err := h.search.Search(c.Request.Context(), &searchv1.SearchRequest{TenantId: request.TenantID, Query: request.Query, DocumentTypes: request.DocumentTypes, ApplicationIds: request.ApplicationIDs, Filters: filters, Sort: sortOrder(request.Sort), Page: request.Page, PageSize: request.PageSize, FacetFields: request.FacetFields}, visibility)
	if err != nil {
		Fail(c, h.logger, searchAppError(err))
		return
	}
	OK(c, searchPageDTO(response))
}

// Suggest godoc
// @Summary Suggest tenant-visible documents
// @Tags search
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body SuggestRequest true "Suggestion prefix"
// @Success 200 {object} Response{body=SuggestResponseDTO}
// @Router /api/v1/search/suggest [post]
func (h *Handler) Suggest(c *gin.Context) {
	var request SuggestRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid suggestion request", err))
		return
	}
	visibility, scopeErr := h.visibility(c, request.TenantID)
	if scopeErr != nil {
		Fail(c, h.logger, scopeErr)
		return
	}
	response, err := h.search.Suggest(c.Request.Context(), &searchv1.SuggestRequest{TenantId: request.TenantID, Prefix: request.Prefix, DocumentTypes: request.DocumentTypes, Limit: request.Limit}, visibility)
	if err != nil {
		Fail(c, h.logger, searchAppError(err))
		return
	}
	OK(c, suggestResponseDTO(response))
}

// GetSearchDocument godoc
// @Summary Get one tenant-visible search document
// @Tags search
// @Accept json
// @Produce json
// @Security Bearer
// @Param request body GetSearchDocumentRequest true "Document identity"
// @Success 200 {object} Response{body=SearchDocumentDTO}
// @Failure 404 {object} Response "Code 10004: document not found"
// @Router /api/v1/search/get [post]
func (h *Handler) GetSearchDocument(c *gin.Context) {
	var request GetSearchDocumentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		Fail(c, h.logger, apperror.Invalid("invalid document request", err))
		return
	}
	visibility, scopeErr := h.visibility(c, request.TenantID)
	if scopeErr != nil {
		Fail(c, h.logger, scopeErr)
		return
	}
	document, err := h.search.Get(c.Request.Context(), request.TenantID, request.ID, visibility)
	if err != nil {
		Fail(c, h.logger, searchAppError(err))
		return
	}
	OK(c, searchDocumentDTO(document))
}

func (h *Handler) visibility(c *gin.Context, tenantID string) ([]string, error) {
	if h.access != nil {
		ctx := searchauth.WithAuthorization(c.Request.Context(), c.GetHeader("Authorization"))
		values, err := h.access.VisibilityTokens(ctx, tenantID)
		if err != nil {
			if errors.Is(err, searchauth.ErrForbidden) {
				return nil, apperror.Forbidden("tenant access denied")
			}
			return nil, apperror.Unavailable("authorization unavailable", err)
		}
		return values, nil
	}
	value, ok := platformprincipal.FromContext(c.Request.Context())
	if !ok {
		return nil, nil
	}
	if value.TenantID != "" && value.TenantID != tenantID {
		return nil, apperror.Forbidden("tenant access denied")
	}
	result := []string{"user:" + value.ID}
	if value.MembershipID != "" {
		result = append(result, "membership:"+value.MembershipID)
	}
	return result, nil
}
func sortOrder(value string) searchv1.SortOrder {
	switch value {
	case "updated_at_desc":
		return searchv1.SortOrder_SORT_ORDER_UPDATED_AT_DESC
	case "updated_at_asc":
		return searchv1.SortOrder_SORT_ORDER_UPDATED_AT_ASC
	default:
		return searchv1.SortOrder_SORT_ORDER_RELEVANCE
	}
}
func searchAppError(err error) error {
	switch {
	case errors.Is(err, searchapp.ErrInvalidArgument):
		return apperror.Invalid(err.Error(), err)
	case errors.Is(err, searchapp.ErrNotFound):
		return apperror.NotFound("search document not found")
	case errors.Is(err, searchapp.ErrUnavailable):
		return apperror.Unavailable("search unavailable", err)
	default:
		return apperror.Internal(err)
	}
}

func searchPageDTO(response *searchv1.SearchResponse) SearchPageDTO {
	result := SearchPageDTO{Total: response.GetTotal(), Page: response.GetPage(), PageSize: response.GetPageSize(), TookMilliseconds: response.GetTookMilliseconds()}
	for _, hit := range response.GetHits() {
		item := SearchHitDTO{Document: searchDocumentDTO(hit.GetDocument()), Score: hit.GetScore()}
		for _, highlight := range hit.GetHighlights() {
			item.Highlights = append(item.Highlights, HighlightDTO{Field: highlight.GetField(), Fragments: highlight.GetFragments()})
		}
		result.Items = append(result.Items, item)
	}
	for _, facet := range response.GetFacets() {
		item := FacetDTO{Field: facet.GetField()}
		for _, bucket := range facet.GetBuckets() {
			item.Buckets = append(item.Buckets, FacetBucketDTO{Value: bucket.GetValue(), Count: bucket.GetCount()})
		}
		result.Facets = append(result.Facets, item)
	}
	return result
}
func suggestResponseDTO(response *searchv1.SuggestResponse) SuggestResponseDTO {
	result := SuggestResponseDTO{}
	for _, item := range response.GetSuggestions() {
		result.Items = append(result.Items, SuggestionDTO{Text: item.GetText(), DocumentType: item.GetDocumentType(), SourceID: item.GetSourceId(), URL: item.GetUrl()})
	}
	return result
}
func searchDocumentDTO(document *searchv1.SearchDocument) SearchDocumentDTO {
	if document == nil {
		return SearchDocumentDTO{}
	}
	result := SearchDocumentDTO{ID: document.GetId(), TenantID: document.GetTenantId(), SourceService: document.GetSourceService(), DocumentType: document.GetDocumentType(), SourceID: document.GetSourceId(), ApplicationID: document.GetApplicationId(), Title: document.GetTitle(), Summary: document.GetSummary(), Content: document.GetContent(), URL: document.GetUrl(), Icon: document.GetIcon(), Keywords: document.GetKeywords(), Attributes: document.GetAttributes(), PermissionCode: document.GetPermissionCode(), SourceVersion: document.GetSourceVersion()}
	if timestamp := document.GetSourceCreatedAt(); timestamp != nil && timestamp.IsValid() {
		value := timestamp.AsTime()
		result.SourceCreatedAt = &value
	}
	if timestamp := document.GetSourceUpdatedAt(); timestamp != nil && timestamp.IsValid() {
		value := timestamp.AsTime()
		result.SourceUpdatedAt = &value
	}
	return result
}
