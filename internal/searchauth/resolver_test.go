package searchauth

import (
	"context"
	"testing"

	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	authorizationv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/authorization/v1"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"google.golang.org/grpc"
)

type clientStub struct{}

func (clientStub) ListBindings(context.Context, *authorizationv1.ListBindingsRequest, ...grpc.CallOption) (*authorizationv1.ListBindingsResponse, error) {
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
