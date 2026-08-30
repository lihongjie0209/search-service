// Package searchauth derives index visibility from the authoritative authorization service.
package searchauth

import (
	"context"
	"errors"
	"fmt"

	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	authorizationv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/authorization/v1"
	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	"google.golang.org/grpc"
)

const pageSize = 200

var ErrForbidden = errors.New("search visibility forbidden")

type Client interface {
	ListBindings(context.Context, *authorizationv1.ListBindingsRequest, ...grpc.CallOption) (*authorizationv1.ListBindingsResponse, error)
}
type Resolver struct{ client Client }

func New(client Client) *Resolver {
	if client == nil {
		return nil
	}
	return &Resolver{client: client}
}

func (r *Resolver) VisibilityTokens(ctx context.Context, tenantID string) ([]string, error) {
	identity, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return nil, platformprincipal.ErrMissing
	}
	if identity.TenantID != "" && identity.TenantID != tenantID {
		return nil, ErrForbidden
	}
	tokens := []string{"user:" + identity.ID}
	if identity.MembershipID != "" {
		tokens = append(tokens, "membership:"+identity.MembershipID)
	}
	if r == nil || identity.MembershipID == "" {
		return tokens, nil
	}
	seen := map[string]struct{}{}
	for page := uint32(1); page <= 100; page++ {
		response, err := r.client.ListBindings(ctx, &authorizationv1.ListBindingsRequest{TenantId: tenantID, Subject: &authorizationv1.Subject{Id: identity.MembershipID, Type: authorizationv1.SubjectType_SUBJECT_TYPE_MEMBERSHIP}, Page: &commonv1.PageRequest{Page: page, PageSize: pageSize}})
		if err != nil {
			return nil, fmt.Errorf("list authorization bindings: %w", err)
		}
		for _, binding := range response.GetBindings() {
			if binding.GetStatus() != "active" || binding.GetRoleId() == "" {
				continue
			}
			if _, ok := seen[binding.GetRoleId()]; ok {
				continue
			}
			seen[binding.GetRoleId()] = struct{}{}
			tokens = append(tokens, "role:"+binding.GetRoleId())
		}
		pageResult := response.GetPage()
		if pageResult == nil || uint64(page)*pageSize >= pageResult.GetTotal() || len(response.GetBindings()) == 0 {
			return tokens, nil
		}
	}
	return nil, fmt.Errorf("authorization bindings exceed pagination safety limit")
}
