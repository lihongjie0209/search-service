package app

import (
	"errors"

	authorizationv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/authorization/v1"
	"github.com/lihongjie0209/search-service/internal/config"
	"github.com/lihongjie0209/search-service/internal/outbound"
	"github.com/lihongjie0209/search-service/internal/searchauth"
	"go.uber.org/fx"
)

func newSearchAccessResolver(cfg config.Config, registry *outbound.Registry) (*searchauth.Resolver, error) {
	connection, ok := registry.GRPC("authorization")
	if !ok {
		if cfg.App.Env == "production" {
			return nil, errors.New("production search requires outbound.grpc.authorization")
		}
		return nil, nil
	}
	return searchauth.New(authorizationv1.NewAuthorizationServiceClient(connection)), nil
}

var SearchAccessModule = fx.Module("search-access", fx.Provide(newSearchAccessResolver))
