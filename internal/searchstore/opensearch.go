package searchstore

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
	"github.com/lihongjie0209/search-service/internal/config"
	"github.com/lihongjie0209/search-service/internal/observability"
	searchapp "github.com/lihongjie0209/search-service/internal/search"
	"github.com/opensearch-project/opensearch-go/v4"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/fx"
	"google.golang.org/protobuf/encoding/protojson"
)

type Engine struct {
	client    *opensearch.Client
	index     string
	metrics   *observability.Metrics
	transport *http.Transport
}

func New(lc fx.Lifecycle, cfg config.Config, metrics *observability.Metrics) (searchapp.Engine, error) {
	if !cfg.OpenSearch.Enabled {
		return nil, errors.New("opensearch must be enabled for search-service")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.OpenSearch.InsecureSkipVerify} //nolint:gosec // production validation forbids this development-only switch.
	client, err := opensearch.NewClient(opensearch.Config{Addresses: cfg.OpenSearch.Addresses, Username: cfg.OpenSearch.Username, Password: cfg.OpenSearch.Password, RequestTimeout: cfg.OpenSearch.Timeout, Transport: otelhttp.NewTransport(transport), MaxRetries: 2, EnableRetryOnTimeout: true})
	if err != nil {
		return nil, fmt.Errorf("create opensearch client: %w", err)
	}
	engine := &Engine{client: client, index: cfg.OpenSearch.Index, metrics: metrics, transport: transport}
	lc.Append(fx.StartHook(func(ctx context.Context) error { return engine.ensureIndex(ctx) }))
	lc.Append(fx.StopHook(func() { transport.CloseIdleConnections() }))
	return engine, nil
}

func (e *Engine) Ping(ctx context.Context) error {
	return e.doJSON(ctx, http.MethodGet, "/_cluster/health", nil, nil)
}

func (e *Engine) ensureIndex(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, "/"+url.PathEscape(e.index), nil)
	if err != nil {
		return err
	}
	response, err := e.client.Transport.Perform(request)
	if err != nil {
		return fmt.Errorf("%w: inspect index: %v", searchapp.ErrUnavailable, err)
	}
	_ = response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	if response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("%w: inspect index status %d", searchapp.ErrUnavailable, response.StatusCode)
	}
	mapping := map[string]any{"settings": map[string]any{"index": map[string]any{"number_of_shards": 1, "number_of_replicas": 1}}, "mappings": map[string]any{"dynamic": false, "properties": map[string]any{
		"id": keyword(), "tenantId": keyword(), "sourceService": keyword(), "documentType": keyword(), "sourceId": keyword(), "applicationId": keyword(),
		"title": textWithKeyword(), "summary": map[string]any{"type": "text"}, "content": map[string]any{"type": "text"}, "url": keyword(), "icon": keyword(),
		"keywords": textWithKeyword(), "attributes": map[string]any{"type": "flat_object"}, "visibilityTokens": keyword(), "permissionCode": keyword(),
		"sourceVersion": map[string]any{"type": "long"}, "sourceCreatedAt": map[string]any{"type": "date"}, "sourceUpdatedAt": map[string]any{"type": "date"},
	}}}
	if err := e.doJSON(ctx, http.MethodPut, "/"+url.PathEscape(e.index), mapping, nil); err != nil {
		var statusErr *responseStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusBadRequest {
			return e.confirmIndex(ctx)
		}
		return fmt.Errorf("create search index: %w", err)
	}
	return nil
}

func (e *Engine) confirmIndex(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, "/"+url.PathEscape(e.index), nil)
	if err != nil {
		return err
	}
	response, err := e.client.Transport.Perform(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("%w: index confirmation status %d", searchapp.ErrUnavailable, response.StatusCode)
}

func keyword() map[string]any { return map[string]any{"type": "keyword", "ignore_above": 2048} }
func textWithKeyword() map[string]any {
	return map[string]any{"type": "text", "fields": map[string]any{"keyword": keyword()}}
}

func (e *Engine) Search(ctx context.Context, request *searchv1.SearchRequest, visibility []string) (*searchv1.SearchResponse, error) {
	body := buildSearchQuery(request, visibility)
	var raw struct {
		Took uint64 `json:"took"`
		Hits struct {
			Total struct {
				Value uint64 `json:"value"`
			} `json:"total"`
			Hits []struct {
				Source    json.RawMessage     `json:"_source"`
				Score     float64             `json:"_score"`
				Highlight map[string][]string `json:"highlight"`
			} `json:"hits"`
		} `json:"hits"`
		Aggregations map[string]struct {
			Buckets []struct {
				Key   string `json:"key"`
				Count uint64 `json:"doc_count"`
			} `json:"buckets"`
		} `json:"aggregations"`
	}
	if err := e.doJSON(ctx, http.MethodPost, "/"+url.PathEscape(e.index)+"/_search", body, &raw); err != nil {
		return nil, err
	}
	response := &searchv1.SearchResponse{Total: raw.Hits.Total.Value, Page: request.Page, PageSize: request.PageSize, TookMilliseconds: raw.Took}
	for _, hit := range raw.Hits.Hits {
		document := new(searchv1.SearchDocument)
		if err := protojson.Unmarshal(hit.Source, document); err != nil {
			return nil, fmt.Errorf("decode search document: %w", err)
		}
		result := &searchv1.SearchHit{Document: document, Score: hit.Score}
		for field, fragments := range hit.Highlight {
			result.Highlights = append(result.Highlights, &searchv1.Highlight{Field: field, Fragments: fragments})
		}
		response.Hits = append(response.Hits, result)
	}
	for field, aggregation := range raw.Aggregations {
		facet := &searchv1.Facet{Field: strings.TrimSuffix(field, "_facet")}
		for _, bucket := range aggregation.Buckets {
			facet.Buckets = append(facet.Buckets, &searchv1.FacetBucket{Value: bucket.Key, Count: bucket.Count})
		}
		response.Facets = append(response.Facets, facet)
	}
	return response, nil
}

func (e *Engine) Suggest(ctx context.Context, request *searchv1.SuggestRequest, visibility []string) (*searchv1.SuggestResponse, error) {
	searchRequest := &searchv1.SearchRequest{TenantId: request.TenantId, Query: request.Prefix, DocumentTypes: request.DocumentTypes, ApplicationIds: request.ApplicationIds, Page: 1, PageSize: request.Limit}
	result, err := e.Search(ctx, searchRequest, visibility)
	if err != nil {
		return nil, err
	}
	response := &searchv1.SuggestResponse{}
	for _, hit := range result.Hits {
		d := hit.Document
		response.Suggestions = append(response.Suggestions, &searchv1.Suggestion{Text: d.Title, DocumentType: d.DocumentType, SourceId: d.SourceId, Url: d.Url})
	}
	return response, nil
}

func (e *Engine) Get(ctx context.Context, tenantID, id string, visibility []string) (*searchv1.SearchDocument, error) {
	request := &searchv1.SearchRequest{TenantId: tenantID, Filters: map[string]*searchv1.StringValues{"id": {Values: []string{id}}}, Page: 1, PageSize: 1}
	response, err := e.Search(ctx, request, visibility)
	if err != nil {
		return nil, err
	}
	if len(response.Hits) == 0 {
		return nil, searchapp.ErrNotFound
	}
	return response.Hits[0].Document, nil
}

func (e *Engine) Upsert(ctx context.Context, documents []*searchv1.SearchDocument) error {
	for _, document := range documents {
		data, err := protojson.Marshal(document)
		if err != nil {
			return err
		}
		path := "/" + url.PathEscape(e.index) + "/_doc/" + url.PathEscape(document.Id) + "?version_type=external_gte&version=" + strconv.FormatInt(document.SourceVersion, 10)
		if err := e.doJSON(ctx, http.MethodPut, path, json.RawMessage(data), nil); err != nil {
			var statusErr *responseStatusError
			if errors.As(err, &statusErr) && statusErr.status == http.StatusConflict {
				continue
			}
			return err
		}
	}
	return nil
}

func (e *Engine) Delete(ctx context.Context, documents []*searchv1.DocumentKey) error {
	for _, document := range documents {
		id := searchapp.CanonicalID(document.TenantId, document.SourceService, document.DocumentType, document.SourceId)
		path := "/" + url.PathEscape(e.index) + "/_doc/" + url.PathEscape(id) + "?version_type=external_gte&version=" + strconv.FormatInt(document.SourceVersion, 10)
		if err := e.doJSON(ctx, http.MethodDelete, path, nil, nil); err != nil && !errors.Is(err, searchapp.ErrNotFound) {
			var statusErr *responseStatusError
			if errors.As(err, &statusErr) && statusErr.status == http.StatusConflict {
				continue
			}
			return err
		}
	}
	return nil
}

func (e *Engine) doJSON(ctx context.Context, method, path string, body any, target any) error {
	started := time.Now()
	status := "error"
	defer func() {
		if e.metrics != nil && e.metrics.Enabled() {
			e.metrics.OutboundRequests.WithLabelValues("opensearch", "search-index", status).Inc()
			e.metrics.OutboundDuration.WithLabelValues("opensearch", "search-index").Observe(time.Since(started).Seconds())
		}
	}()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := e.client.Transport.Perform(request)
	if err != nil {
		return fmt.Errorf("%w: %v", searchapp.ErrUnavailable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		status = strconv.Itoa(response.StatusCode)
		return searchapp.ErrNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status = strconv.Itoa(response.StatusCode)
		_, _ = io.Copy(io.Discard, response.Body)
		return &responseStatusError{status: response.StatusCode}
	}
	status = strconv.Itoa(response.StatusCode)
	if target != nil {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			return fmt.Errorf("decode opensearch response: %w", err)
		}
	}
	return nil
}

type responseStatusError struct{ status int }

func (e *responseStatusError) Error() string {
	return fmt.Sprintf("opensearch request failed with status %d", e.status)
}
func (e *responseStatusError) Unwrap() error { return searchapp.ErrUnavailable }

func buildSearchQuery(request *searchv1.SearchRequest, visibility []string) map[string]any {
	filters := []any{map[string]any{"term": map[string]any{"tenantId": request.TenantId}}, map[string]any{"terms": map[string]any{"visibilityTokens": visibility}}}
	if len(request.DocumentTypes) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"documentType": request.DocumentTypes}})
	}
	if len(request.ApplicationIds) > 0 {
		filters = append(filters, map[string]any{"terms": map[string]any{"applicationId": request.ApplicationIds}})
	}
	for field, values := range request.Filters {
		if allowedFilter(field) && values != nil && len(values.Values) > 0 {
			filters = append(filters, map[string]any{"terms": map[string]any{field: values.Values}})
		}
	}
	must := []any{map[string]any{"match_all": map[string]any{}}}
	if strings.TrimSpace(request.Query) != "" {
		must = []any{map[string]any{"multi_match": map[string]any{"query": request.Query, "fields": []string{"title^4", "keywords^3", "summary^2", "content"}, "type": "best_fields"}}}
	}
	body := map[string]any{"from": int((request.Page - 1) * request.PageSize), "size": request.PageSize, "track_total_hits": true, "query": map[string]any{"bool": map[string]any{"must": must, "filter": filters}}, "highlight": map[string]any{"encoder": "html", "fields": map[string]any{"title": map[string]any{}, "summary": map[string]any{}, "content": map[string]any{}}, "pre_tags": []string{"<mark>"}, "post_tags": []string{"</mark>"}}}
	if request.Sort == searchv1.SortOrder_SORT_ORDER_UPDATED_AT_DESC {
		body["sort"] = []any{map[string]any{"sourceUpdatedAt": "desc"}}
	}
	if request.Sort == searchv1.SortOrder_SORT_ORDER_UPDATED_AT_ASC {
		body["sort"] = []any{map[string]any{"sourceUpdatedAt": "asc"}}
	}
	aggs := map[string]any{}
	for _, field := range request.FacetFields {
		if allowedFilter(field) {
			aggs[field+"_facet"] = map[string]any{"terms": map[string]any{"field": field, "size": 50}}
		}
	}
	if len(aggs) > 0 {
		body["aggs"] = aggs
	}
	return body
}

func allowedFilter(field string) bool {
	switch field {
	case "id", "sourceService", "documentType", "applicationId", "permissionCode":
		return true
	default:
		return strings.HasPrefix(field, "attributes.") && len(field) <= 128
	}
}

var Module = fx.Module("opensearch", fx.Provide(New))
