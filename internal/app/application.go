package app

import (
	"context"
	"errors"
	"time"

	"github.com/lihongjie0209/microservice-platform-go/appaccess"
	applicationv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/application/v1"
	"github.com/lihongjie0209/search-service/internal/config"
	"github.com/lihongjie0209/search-service/internal/outbound"
	searchapp "github.com/lihongjie0209/search-service/internal/search"
)

type disabledApplicationVerifier struct{}

func (disabledApplicationVerifier) Check(_ context.Context, _ string, applicationIDs []string) (map[string]appaccess.Decision, error) {
	result := make(map[string]appaccess.Decision, len(applicationIDs))
	for _, applicationID := range applicationIDs {
		result[applicationID] = appaccess.Decision{Granted: true}
	}
	return result, nil
}

func newApplicationVerifier(cfg config.Config, registry *outbound.Registry) (searchapp.ApplicationAccess, error) {
	if !cfg.OpenSearch.Enabled {
		return disabledApplicationVerifier{}, nil
	}
	if registry == nil {
		return nil, errors.New("search service requires outbound registry")
	}
	connection, ok := registry.GRPC("application")
	if !ok {
		return nil, errors.New("search service requires outbound.grpc.application")
	}
	return appaccess.NewGRPCVerifier(applicationv1.NewApplicationServiceClient(connection), 2*time.Second), nil
}
