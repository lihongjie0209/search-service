package searchauth

import (
	"context"
	"testing"

	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	authorizationv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/authorization/v1"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type clientStub struct{ authorization string }

func (s clientStub) ListBindings(ctx context.Context, _ *authorizationv1.ListBindingsRequest, _ ...grpc.CallOption) (*authorizationv1.ListBindingsResponse, error) {
	if outgoing, ok := metadata.FromOutgoingContext(ctx); ok && len(outgoing.Get("authorization")) > 0 {
		values := outgoing.Get("authorization")
		s.authorization = values[0]
	}
	return &authorizationv1.ListBindingsResponse{Bindings: []*authorizationv1.Binding{{RoleId: "admin", Status: "active"}, {RoleId: "revoked", Status: "revoked"}}, Page: &commonv1.PageResult{Total: 2}}, nil
}

func TestVisibilityTokensAreServerDerived(t *testing.T) {
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})
	tokens, err := New(clientStub{}).VisibilityTokens(ctx, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 3 || tokens[2] != "role:admin" {
		t.Fatalf("tokens = %v", tokens)
	}
	if _, err := New(clientStub{}).VisibilityTokens(ctx, "tenant-2"); err == nil {
		t.Fatal("cross-tenant visibility was accepted")
	}
}

func TestForwardIncomingAuthorization(t *testing.T) {
	ctx := platformprincipal.WithContext(t.Context(), platformprincipal.Principal{ID: "user-1", Type: platformprincipal.TypeUser, TenantID: "tenant-1", MembershipID: "member-1"})
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer caller-token"))
	client := &capturingClient{}
	if _, err := New(client).VisibilityTokens(ctx, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	if client.authorization != "Bearer caller-token" {
		t.Fatalf("authorization=%q", client.authorization)
	}
}

type capturingClient struct{ authorization string }

func (c *capturingClient) ListBindings(ctx context.Context, _ *authorizationv1.ListBindingsRequest, _ ...grpc.CallOption) (*authorizationv1.ListBindingsResponse, error) {
	outgoing, _ := metadata.FromOutgoingContext(ctx)
	values := outgoing.Get("authorization")
	if len(values) > 0 {
		c.authorization = values[0]
	}
	return &authorizationv1.ListBindingsResponse{Page: &commonv1.PageResult{}}, nil
}
