package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfig_AuthorizationRequiresConfiguredUpstream(t *testing.T) {
	cfg, err := Load("../../config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Authorization.Enabled = true
	delete(cfg.Outbound.GRPC, "authorization")
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "outbound.grpc.authorization") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfig_OpenSearchRequiresApplicationUpstream(t *testing.T) {
	cfg, err := Load("../../config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.OpenSearch.Enabled = true
	cfg.OpenSearch.Addresses = []string{"http://127.0.0.1:9200"}
	delete(cfg.Outbound.GRPC, "application")
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "outbound.grpc.application") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfig_ProductionRequiresAuthorization(t *testing.T) {
	cfg, err := Load("../../config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.App.Env = "production"
	cfg.GRPC.Enabled = false
	cfg.GRPC.ReflectionEnabled = false
	cfg.Swagger.RequireAuth = true
	cfg.Auth.JWKSURL = "https://identity.example.test/.well-known/jwks.json"
	cfg.Authorization.Enabled = false
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "authorization must be enabled") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoad_EnvironmentOverridesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("http:\n  address: 127.0.0.1:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_HTTP_ADDRESS", "127.0.0.1:9090")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTP.Address != "127.0.0.1:9090" {
		t.Fatalf("HTTP.Address = %q, want %q", cfg.HTTP.Address, "127.0.0.1:9090")
	}
}

func TestLoad_OpenSearchAddressesFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("opensearch:\n  enabled: true\noutbound:\n  grpc:\n    application:\n      target: 127.0.0.1:9090\n      timeout: 1s\n      retry: {max_attempts: 1, initial_backoff: 1ms, max_backoff: 1ms}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_OPENSEARCH_ADDRESSES", "http://search-a:9200,http://search-b:9200")
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.OpenSearch.Addresses) != 2 || cfg.OpenSearch.Addresses[1] != "http://search-b:9200" {
		t.Fatalf("addresses = %v", cfg.OpenSearch.Addresses)
	}
}

func TestConfig_ValidateJWTSecret(t *testing.T) {
	t.Parallel()
	cfg := Config{HTTP: HTTP{Address: "127.0.0.1:8080"}, Auth: Auth{ClientID: "client", ClientSecret: "secret"}, JWT: JWT{Secret: "short"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestLoadWithProfile_MergesProfileThenEnvironment(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "config.yaml")
	profile := filepath.Join(dir, "config-test.yaml")
	if err := os.WriteFile(base, []byte("app:\n  env: development\nlog:\n  level: info\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile, []byte("log:\n  level: debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_LOG_LEVEL", "error")
	cfg, err := LoadWithProfile(base, "test")
	if err != nil {
		t.Fatalf("LoadWithProfile() error = %v", err)
	}
	if cfg.App.Env != "test" || cfg.Runtime.ActiveProfile != "test" {
		t.Fatalf("active profile = %q/%q", cfg.App.Env, cfg.Runtime.ActiveProfile)
	}
	if cfg.Log.Level != "error" {
		t.Fatalf("Log.Level = %q, want environment override", cfg.Log.Level)
	}
	if len(cfg.Runtime.ConfigFiles) != 2 || cfg.Runtime.ConfigFiles[1] != profile {
		t.Fatalf("ConfigFiles = %v", cfg.Runtime.ConfigFiles)
	}
}

func TestConfig_ValidateAuthSkipPattern(t *testing.T) {
	t.Parallel()
	cfg := Config{HTTP: HTTP{Address: "127.0.0.1:8080", RequestTimeout: time.Second}, Health: Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second}, Auth: Auth{SkipHTTPPaths: []string{"/api/v1/[broken"}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want invalid wildcard error")
	}
}

func TestConfig_ValidateAutoMigration(t *testing.T) {
	t.Parallel()
	cfg := Config{
		HTTP:      HTTP{Address: "127.0.0.1:8080", RequestTimeout: time.Second},
		Health:    Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second},
		Migration: Migration{AutoUp: true, Path: "migrations/postgres"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want auto migration dependency error")
	}
}

func TestConfig_ValidateEventConsumerDeadline(t *testing.T) {
	cfg := Config{App: App{Env: "test"}, HTTP: HTTP{Address: "127.0.0.1:8080", RequestTimeout: time.Second}, Health: Health{DatabaseTimeout: time.Second, RedisTimeout: time.Second, OpenSearchTimeout: time.Second}, Database: Database{Enabled: true, Name: "platform", Type: "postgres", DSN: "postgres://example"}, Projection: Projection{Durable: "search", Subject: "platform.search.>"}, EventBus: EventBus{Enabled: true, URLs: []string{"nats://localhost:4222"}, StreamName: "EVENTS", Subjects: []string{"platform.>"}, Storage: "memory", MaxAge: time.Hour, DuplicateWindow: time.Minute, ConnectTimeout: time.Second, ReconnectWait: time.Second, PublishTimeout: time.Second, ConsumerAckWait: 5 * time.Second, ConsumerAckTimeout: time.Second, ConsumerHandlerTimeout: 5 * time.Second, ConsumerRetryDelay: time.Second, ConsumerMaxRetryDelay: time.Second, ConsumerMaxDeliver: 3, ConsumerMaxAckPending: 8, DeadLetterSubject: "platform.system.event.dead-lettered.v1", DeadLetterMaxDataBytes: 1024}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("handler timeout equal to ack wait was accepted")
	}
}
func TestLoad_UsesCanonicalPlatformEventStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("http:\n  address: 127.0.0.1:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EventBus.StreamName != "PLATFORM_EVENTS" || len(cfg.EventBus.Subjects) != 1 || cfg.EventBus.Subjects[0] != "platform.>" {
		t.Fatalf("unexpected event stream defaults: %q %#v", cfg.EventBus.StreamName, cfg.EventBus.Subjects)
	}
	if cfg.EventBus.InboxRetention != 14*24*time.Hour || cfg.EventBus.InboxCleanupInterval != time.Hour || cfg.EventBus.InboxCleanupBatchSize != 500 {
		t.Fatalf("unexpected inbox retention defaults: %+v", cfg.EventBus)
	}
}
