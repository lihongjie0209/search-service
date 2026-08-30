package searchstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
	"github.com/opensearch-project/opensearch-go/v4"
)

func TestBuildSearchQueryEnforcesTenantVisibilityAndAllowlist(t *testing.T) {
	body := buildSearchQuery(&searchv1.SearchRequest{TenantId: "tenant-1", Query: "invoice", Page: 1, PageSize: 20, Filters: map[string]*searchv1.StringValues{"sourceService": {Values: []string{"billing"}}, "unsafe.script": {Values: []string{"x"}}}}, []string{"tenant:tenant-1:*", "user:u1"})
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	for _, expected := range []string{"tenantId", "tenant-1", "visibilityTokens", "user:u1", "sourceService"} {
		if !strings.Contains(value, expected) {
			t.Fatalf("query missing %q: %s", expected, value)
		}
	}
	if strings.Contains(value, "unsafe.script") {
		t.Fatalf("query contains unapproved filter: %s", value)
	}
}

func TestUpsertUsesExternalVersion(t *testing.T) {
	var path, query string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path, query = request.URL.Path, request.URL.RawQuery
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"result":"created"}`))
	}))
	defer server.Close()
	client, err := opensearch.NewClient(opensearch.Config{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{client: client, index: "documents"}
	err = engine.Upsert(context.Background(), []*searchv1.SearchDocument{{Id: "tenant:svc:type:1", SourceVersion: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/documents/_doc/tenant:svc:type:1" || query != "version_type=external_gte&version=7" {
		t.Fatalf("request = %s?%s", path, query)
	}
}

func TestUpsertTreatsStaleExternalVersionAsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusConflict) }))
	defer server.Close()
	client, err := opensearch.NewClient(opensearch.Config{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{client: client, index: "documents"}
	if err := engine.Upsert(t.Context(), []*searchv1.SearchDocument{{Id: "document", SourceVersion: 1}}); err != nil {
		t.Fatalf("stale event must be ignored: %v", err)
	}
}
