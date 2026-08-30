//go:build integration

package integration

import (
	"net/http"
	"testing"
	"time"

	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
	"github.com/lihongjie0209/search-service/internal/config"
	"github.com/lihongjie0209/search-service/internal/observability"
	searchapp "github.com/lihongjie0209/search-service/internal/search"
	"github.com/lihongjie0209/search-service/internal/searchstore"
	"github.com/testcontainers/testcontainers-go"
	opensearchcontainer "github.com/testcontainers/testcontainers-go/modules/opensearch"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestOpenSearchProjectionAndTenantVisibility(t *testing.T) {
	container, err := opensearchcontainer.Run(t.Context(), "opensearchproject/opensearch:3.6.0")
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	address, err := container.Address(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{OpenSearch: config.OpenSearch{Enabled: true, Addresses: []string{address}, Index: "search-integration", Timeout: 10 * time.Second}}
	var engine searchapp.Engine
	application := fxtest.New(t, fx.Supply(cfg, observability.NewMetrics(cfg, nil, nil)), fx.Provide(searchstore.New), fx.Populate(&engine))
	application.RequireStart()
	t.Cleanup(application.RequireStop)
	document := &searchv1.SearchDocument{Id: "tenant-1:apps:application:app-1", TenantId: "tenant-1", SourceService: "application-service", DocumentType: "application", SourceId: "app-1", Title: "Billing Console", Summary: "Invoices and payments", SourceVersion: 1, VisibilityTokens: []string{"user:user-1"}}
	if err := engine.Upsert(t.Context(), []*searchv1.SearchDocument{document}); err != nil {
		t.Fatal(err)
	}
	// Refresh is intentionally explicit in tests; production indexing remains refresh-free.
	client, err := http.NewRequestWithContext(t.Context(), http.MethodPost, address+"/search-integration/_refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(client)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	result, err := engine.Search(t.Context(), &searchv1.SearchRequest{TenantId: "tenant-1", Query: "billing", Page: 1, PageSize: 20}, []string{"user:user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.GetTotal() != 1 || result.GetHits()[0].GetDocument().GetSourceId() != "app-1" {
		t.Fatalf("result = %#v", result)
	}
	hidden, err := engine.Search(t.Context(), &searchv1.SearchRequest{TenantId: "tenant-1", Query: "billing", Page: 1, PageSize: 20}, []string{"user:other"})
	if err != nil {
		t.Fatal(err)
	}
	if hidden.GetTotal() != 0 {
		t.Fatalf("unauthorized result total = %d", hidden.GetTotal())
	}
}
