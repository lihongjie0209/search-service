//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	platformeventbus "github.com/lihongjie0209/microservice-platform-go/eventbus"
	applicationv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/application/v1"
	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
	"github.com/lihongjie0209/search-service/internal/app"
	"github.com/lihongjie0209/search-service/internal/auth"
	"github.com/lihongjie0209/search-service/internal/config"
	serviceeventbus "github.com/lihongjie0209/search-service/internal/eventbus"
	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	opensearchcontainer "github.com/testcontainers/testcontainers-go/modules/opensearch"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	rediscontainer "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
)

func TestHTTPAndGRPCEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	postgresContainer, err := postgres.Run(ctx, "postgres:17-alpine", postgres.WithDatabase("app"), postgres.WithUsername("app"), postgres.WithPassword("app"), postgres.BasicWaitStrategies(), postgres.WithSQLDriver("pgx"))
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, postgresContainer)
	dsn, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	migrationPath, _ := filepath.Abs(filepath.Join("..", "migrations", "postgres"))

	redisContainer, err := rediscontainer.Run(ctx, "redis:7.4-alpine")
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, redisContainer)
	redisURL, err := redisContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	redisOptions, err := goredis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	opensearchContainer, err := opensearchcontainer.Run(ctx, "opensearchproject/opensearch:3.6.0")
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, opensearchContainer)
	opensearchAddress, err := opensearchContainer.Address(ctx)
	if err != nil {
		t.Fatal(err)
	}
	natsURL := startNATS(t, ctx)

	httpAddress := freeAddress(t)
	grpcAddress := freeAddress(t)
	const secret = "01234567890123456789012345678901"
	applicationAddress := startAllowApplicationServer(t)
	cfg := config.Config{
		Runtime:       config.Runtime{ActiveProfile: "integration"},
		App:           config.App{Name: "integration", Env: "integration", ShutdownTimeout: 10 * time.Second},
		HTTP:          config.HTTP{Address: httpAddress, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, RequestTimeout: 5 * time.Second, MaxBodyBytes: 1 << 20},
		GRPC:          config.GRPC{Enabled: true, Address: grpcAddress, MaxReceiveBytes: 4 << 20},
		Log:           config.Log{Level: "error", Format: "json", File: filepath.Join(t.TempDir(), "app.log"), MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 1},
		Database:      config.Database{Enabled: true, Type: "postgres", DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute, PingTimeout: 10 * time.Second},
		Migration:     config.Migration{AutoUp: true, Path: migrationPath, DatabaseURL: dsn, Table: "integration_e2e_schema_migrations"},
		Redis:         config.Redis{Enabled: true, Address: redisOptions.Addr, DB: redisOptions.DB, DialTimeout: 5 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second},
		Health:        config.Health{DatabaseTimeout: 2 * time.Second, RedisTimeout: 2 * time.Second, OpenSearchTimeout: 2 * time.Second},
		OpenSearch:    config.OpenSearch{Enabled: true, Addresses: []string{opensearchAddress}, Index: "search-e2e", Timeout: 10 * time.Second},
		EventBus:      config.EventBus{Enabled: true, URLs: []string{natsURL}, StreamName: "PLATFORM_EVENTS", Subjects: []string{"platform.>"}, Storage: "memory", MaxAge: time.Hour, DuplicateWindow: time.Minute, ConnectTimeout: 5 * time.Second, ReconnectWait: time.Second, PublishTimeout: 5 * time.Second, ConsumerAckWait: 10 * time.Second, ConsumerAckTimeout: time.Second, ConsumerHandlerTimeout: 5 * time.Second, ConsumerRetryDelay: 100 * time.Millisecond, ConsumerMaxRetryDelay: time.Second, ConsumerMaxDeliver: 3, ConsumerMaxAckPending: 8, DeadLetterSubject: "platform.system.event.dead-lettered.v1", DeadLetterMaxDataBytes: 1024, InboxRetention: 14 * 24 * time.Hour, InboxCleanupInterval: time.Hour, InboxCleanupBatchSize: 500},
		Projection:    config.Projection{Durable: "search-e2e", Subject: "platform.search.document.>"},
		Observability: config.Observability{MetricsEnabled: true},
		JWT:           config.JWT{Issuer: "integration", Secret: secret, TTL: time.Hour},
		Auth:          config.Auth{ClientID: "client", ClientSecret: "secret", SkipHTTPPaths: []string{"/api/v1/version"}, SkipGRPCMethods: []string{"/grpc.health.v1.Health/*"}, PSK: config.PSK{Enabled: true, Key: secret, GRPCMethods: []string{"/platform.search.v1.SearchService/Batch*"}}},
		Idempotency:   config.Idempotency{Enabled: true, ProcessingTTL: 30 * time.Second, ResultTTL: time.Hour, FailureTTL: time.Minute},
		Outbound:      config.Outbound{GRPC: map[string]config.GRPCUpstream{"application": {Target: applicationAddress, Timeout: 2 * time.Second, Retry: config.Retry{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}}},
	}
	application := app.New(cfg)
	if err := application.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = application.Stop(stopCtx)
	})
	token, err := auth.New(cfg).Issue("client")
	if err != nil {
		t.Fatal(err)
	}

	baseURL := "http://" + httpAddress
	if status := postJSON(t, baseURL+"/api/v1/version", "", "", `{}`); status != http.StatusOK {
		t.Fatalf("public version status = %d", status)
	}
	if status := postJSON(t, baseURL+"/api/v1/me", "Bearer "+token, "", `{}`); status != http.StatusOK {
		t.Fatalf("JWT status = %d", status)
	}

	connection, err := grpc.NewClient(grpcAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	healthResponse, err := grpc_health_v1.NewHealthClient(connection).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil || healthResponse.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("health = %v, %v", healthResponse, err)
	}
	searchClient := searchv1.NewSearchServiceClient(connection)
	pskCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "PSK "+secret)
	document := &searchv1.SearchDocument{Id: "tenant-1:app:application:1", TenantId: "tenant-1", ApplicationId: "app-1", SourceService: "application-service", DocumentType: "application", SourceId: "1", Title: "Platform Console", SourceVersion: 1, VisibilityTokens: []string{"user:client"}}
	if _, err := searchClient.BatchUpsertDocuments(pskCtx, &searchv1.BatchUpsertDocumentsRequest{Documents: []*searchv1.SearchDocument{document}}); err != nil {
		t.Fatalf("PSK upsert: %v", err)
	}
	refresh, _ := http.NewRequestWithContext(ctx, http.MethodPost, opensearchAddress+"/search-e2e/_refresh", nil)
	refreshResponse, err := http.DefaultClient.Do(refresh)
	if err != nil {
		t.Fatal(err)
	}
	refreshResponse.Body.Close()
	if status := postJSON(t, baseURL+"/api/v1/search/query", "Bearer "+token, "", `{"tenant_id":"tenant-1","application_ids":["app-1"],"query":"console"}`); status != http.StatusOK {
		t.Fatalf("HTTP search status = %d", status)
	}
	publisher, err := serviceeventbus.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publisher.Close() })
	eventDocument := &searchv1.SearchDocument{TenantId: "tenant-1", SourceService: "application-service", DocumentType: "application", SourceId: "event-1", Title: "Event Indexed Catalog", SourceVersion: 1, VisibilityTokens: []string{"user:client"}}
	envelope, err := platformeventbus.NewEnvelope(platformeventbus.Metadata{EventID: "search-event-1", EventType: "platform.search.document.upserted.v1", AggregateID: "event-1", AggregateType: "search_document", TenantID: "tenant-1", SchemaVersion: 1}, &searchv1.SearchDocumentUpsertedEvent{Document: eventDocument})
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(ctx, envelope.GetEventType(), envelope); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		var inboxCount int
		database, err := sqlx.ConnectContext(ctx, "pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		err = database.GetContext(ctx, &inboxCount, `SELECT COUNT(*) FROM search_event_inbox WHERE event_id='search-event-1' AND status='completed'`)
		database.Close()
		if err != nil {
			t.Fatal(err)
		}
		if inboxCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("event was not recorded by Inbox")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type allowApplicationServer struct {
	applicationv1.UnimplementedApplicationServiceServer
}

func (allowApplicationServer) BatchCheckTenantApplications(_ context.Context, request *applicationv1.BatchCheckTenantApplicationsRequest) (*applicationv1.BatchCheckTenantApplicationsResponse, error) {
	decisions := make([]*applicationv1.TenantApplicationDecision, 0, len(request.GetApplicationIds()))
	for _, applicationID := range request.GetApplicationIds() {
		decisions = append(decisions, &applicationv1.TenantApplicationDecision{ApplicationId: applicationID, Granted: true})
	}
	return &applicationv1.BatchCheckTenantApplicationsResponse{Decisions: decisions}, nil
}

func startAllowApplicationServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	applicationv1.RegisterApplicationServiceServer(server, allowApplicationServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	return listener.Addr().String()
}

func startNATS(t *testing.T, ctx context.Context) string {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "nats:2.14.6-alpine", ExposedPorts: []string{"4222/tcp"}, Cmd: []string{"--jetstream", "--store_dir=/data"}, WaitingFor: wait.ForLog("Server is ready").WithStartupTimeout(time.Minute)}, Started: true})
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "4222/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return "nats://" + net.JoinHostPort(host, port.Port())
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func postJSON(t *testing.T, target, authorization, key, body string) int {
	t.Helper()
	_, status := postJSONBody(t, target, authorization, key, body)
	return status
}
func postJSONBody(t *testing.T, target, authorization, key, body string) ([]byte, int) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var validJSON any
	if err := json.Unmarshal(data, &validJSON); err != nil {
		t.Fatalf("invalid JSON response: %v (%s)", err, data)
	}
	return data, response.StatusCode
}

var _ = fmt.Sprintf
