package grpctransport

import (
	"slices"
	"testing"
	"time"

	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
	"github.com/lihongjie0209/search-service/internal/auth"
	"github.com/lihongjie0209/search-service/internal/config"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestConfiguredIdempotencyOnlyProtectsProjectionMutations(t *testing.T) {
	cfg, err := config.Load("../../../config/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{searchv1.SearchService_BatchUpsertDocuments_FullMethodName, searchv1.SearchService_BatchDeleteDocuments_FullMethodName}
	for _, method := range want {
		if !slices.Contains(cfg.Idempotency.GRPCMethods, method) {
			t.Fatalf("missing mutation method %q", method)
		}
	}
	for _, method := range []string{searchv1.SearchService_Search_FullMethodName, searchv1.SearchService_Suggest_FullMethodName, searchv1.SearchService_GetDocument_FullMethodName} {
		if slices.Contains(cfg.Idempotency.GRPCMethods, method) {
			t.Fatalf("query method %q must not be idempotency-cached", method)
		}
	}
}

func TestSearchGRPCRequirementCoversMethodsAndScopes(t *testing.T) {
	t.Parallel()
	resolve := searchGRPCRequirement(true)
	for _, method := range []string{searchv1.SearchService_Search_FullMethodName, searchv1.SearchService_Suggest_FullMethodName, searchv1.SearchService_GetDocument_FullMethodName, searchv1.SearchService_BatchUpsertDocuments_FullMethodName, searchv1.SearchService_BatchDeleteDocuments_FullMethodName} {
		requirement, ok := resolve(method)
		if !ok || requirement.Resource == "" || requirement.Action == "" {
			t.Fatalf("method %q requirement = %+v, %v", method, requirement, ok)
		}
	}
	query, _ := resolve(searchv1.SearchService_Search_FullMethodName)
	upsert, _ := resolve(searchv1.SearchService_BatchUpsertDocuments_FullMethodName)
	if query.Scope != platformauthz.ScopePrincipal || upsert.Scope != platformauthz.ScopePlatform {
		t.Fatalf("unexpected scopes: query=%v upsert=%v", query.Scope, upsert.Scope)
	}
	if _, ok := searchGRPCRequirement(false)(searchv1.SearchService_Search_FullMethodName); ok {
		t.Fatal("disabled authorization must not enforce")
	}
}

func TestAuthenticateGRPC_PSKWildcard(t *testing.T) {
	t.Parallel()
	const key = "01234567890123456789012345678901"
	authService := auth.New(config.Config{JWT: config.JWT{Issuer: "test", Secret: key, TTL: time.Hour}})
	cfg := config.Auth{
		SkipGRPCMethods: []string{"/hello.v1.UserService/*"},
		PSK:             config.PSK{Enabled: true, Key: key, GRPCMethods: []string{"/hello.v1.UserService/*"}},
	}
	for _, test := range []struct {
		name   string
		header string
		code   codes.Code
	}{
		{name: "valid", header: "PSK " + key, code: codes.OK},
		{name: "PSK precedes skip", code: codes.Unauthenticated},
		{name: "bearer rejected", header: "Bearer " + key, code: codes.Unauthenticated},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", test.header))
			authenticated, err := authenticateGRPC(ctx, "/hello.v1.UserService/GetUser", authService, cfg)
			if got := status.Code(err); got != test.code {
				t.Fatalf("status code = %s, want %s", got, test.code)
			}
			if test.code == codes.OK {
				value, ok := platformprincipal.FromContext(authenticated)
				if !ok || value.ID != "search-service:psk" || value.Type != platformprincipal.TypeServiceAccount {
					t.Fatalf("principal = %#v, %v", value, ok)
				}
			}
		})
	}
}

func TestAuthenticateGRPC_JWTInjectsPrincipal(t *testing.T) {
	t.Parallel()
	const key = "01234567890123456789012345678901"
	service := auth.New(config.Config{JWT: config.JWT{Issuer: "test", Secret: key, TTL: time.Hour}, Auth: config.Auth{ClientID: "client", ClientSecret: "secret"}})
	token, err := service.Issue("user-1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+token))
	ctx, err = authenticateGRPC(ctx, "/hello.v1.UserService/GetUser", service, config.Auth{})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := platformprincipal.FromContext(ctx)
	if !ok || value.ID != "user-1" || value.Type != platformprincipal.TypeUser {
		t.Fatalf("principal = %#v, %v", value, ok)
	}
}

func TestRequireIndexerRejectsUser(t *testing.T) {
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser})
	if code := status.Code(requireIndexer(ctx)); code != codes.PermissionDenied {
		t.Fatalf("code = %s", code)
	}
	ctx = platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "service-1", Type: platformprincipal.TypeServiceAccount})
	if err := requireIndexer(ctx); err != nil {
		t.Fatal(err)
	}
}
