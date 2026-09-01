package grpctransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	platformauthz "github.com/lihongjie0209/microservice-platform-go/authz"
	platformprincipal "github.com/lihongjie0209/microservice-platform-go/principal"
	searchv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/search/v1"
	"github.com/lihongjie0209/search-service/internal/auth"
	"github.com/lihongjie0209/search-service/internal/config"
	"github.com/lihongjie0209/search-service/internal/environment"
	apphealth "github.com/lihongjie0209/search-service/internal/health"
	"github.com/lihongjie0209/search-service/internal/idempotency"
	"github.com/lihongjie0209/search-service/internal/observability"
	"github.com/lihongjie0209/search-service/internal/requestid"
	searchapp "github.com/lihongjie0209/search-service/internal/search"
	"github.com/lihongjie0209/search-service/internal/searchauth"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type Server struct {
	server  *grpc.Server
	address string
	logger  *slog.Logger
}

func NewServer(lc fx.Lifecycle, cfg config.Config, authService *auth.Service, authorizer platformauthz.Authorizer, healthService *apphealth.Service, searchService *searchapp.Service, access *searchauth.Resolver, metrics *observability.Metrics, logger *slog.Logger) (*Server, error) {
	options := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.GRPC.MaxReceiveBytes),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(environmentInterceptor(cfg.Runtime.ActiveProfile), requestIDInterceptor, idempotencyInterceptor, recoveryInterceptor(logger), authInterceptor(authService, cfg.Auth), platformauthz.UnaryServerInterceptor(authorizer, searchGRPCRequirement(cfg.Authorization.Enabled)), metricsInterceptor(metrics, logger)),
		grpc.ChainStreamInterceptor(environmentStreamInterceptor(cfg.Runtime.ActiveProfile), requestIDStreamInterceptor, idempotencyStreamInterceptor, recoveryStreamInterceptor(logger), authStreamInterceptor(authService, cfg.Auth), metricsStreamInterceptor(metrics, logger)),
	}
	if cfg.GRPC.TLS.Enabled {
		creds, err := serverCredentials(cfg.GRPC.TLS)
		if err != nil {
			return nil, err
		}
		options = append(options, grpc.Creds(creds))
	}
	grpcServer := grpc.NewServer(options...)
	searchv1.RegisterSearchServiceServer(grpcServer, &searchServer{service: searchService, access: access})
	grpc_health_v1.RegisterHealthServer(grpcServer, &healthServer{health: healthService})
	if cfg.GRPC.ReflectionEnabled {
		reflection.Register(grpcServer)
	}
	server := &Server{server: grpcServer, address: cfg.GRPC.Address, logger: logger}
	lc.Append(fx.Hook{OnStart: server.start(cfg.GRPC.Enabled), OnStop: server.stop})
	return server, nil
}

func searchGRPCRequirement(enabled bool) platformauthz.GRPCResolver {
	return func(method string) (platformauthz.Requirement, bool) {
		if !enabled {
			return platformauthz.Requirement{}, false
		}
		requirements := map[string]platformauthz.Requirement{
			searchv1.SearchService_Search_FullMethodName:               {Resource: "search.document", Action: "search", Scope: platformauthz.ScopePrincipal},
			searchv1.SearchService_Suggest_FullMethodName:              {Resource: "search.document", Action: "suggest", Scope: platformauthz.ScopePrincipal},
			searchv1.SearchService_GetDocument_FullMethodName:          {Resource: "search.document", Action: "read", Scope: platformauthz.ScopePrincipal},
			searchv1.SearchService_BatchUpsertDocuments_FullMethodName: {Resource: "search.index", Action: "upsert", Scope: platformauthz.ScopePlatform},
			searchv1.SearchService_BatchDeleteDocuments_FullMethodName: {Resource: "search.index", Action: "delete", Scope: platformauthz.ScopePlatform},
		}
		requirement, ok := requirements[method]
		return requirement, ok
	}
}

func (s *Server) start(enabled bool) func(context.Context) error {
	return func(context.Context) error {
		if !enabled {
			s.logger.Warn("grpc server is disabled")
			return nil
		}
		listener, err := net.Listen("tcp", s.address)
		if err != nil {
			return fmt.Errorf("listen grpc: %w", err)
		}
		go func() {
			if err := s.server.Serve(listener); err != nil {
				s.logger.Error("grpc server stopped unexpectedly", "error", err)
			}
		}()
		s.logger.Info("grpc server started", "address", s.address)
		return nil
	}
}
func (s *Server) stop(ctx context.Context) error {
	stopped := make(chan struct{})
	go func() { s.server.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		s.server.Stop()
		return ctx.Err()
	}
}

type searchServer struct {
	searchv1.UnimplementedSearchServiceServer
	service *searchapp.Service
	access  *searchauth.Resolver
}

func (s *searchServer) Search(ctx context.Context, request *searchv1.SearchRequest) (*searchv1.SearchResponse, error) {
	visibility, err := callerVisibility(ctx, request.GetTenantId(), s.access)
	if err != nil {
		return nil, err
	}
	result, err := s.service.Search(ctx, request, visibility)
	return result, searchError(err)
}
func (s *searchServer) Suggest(ctx context.Context, request *searchv1.SuggestRequest) (*searchv1.SuggestResponse, error) {
	visibility, err := callerVisibility(ctx, request.GetTenantId(), s.access)
	if err != nil {
		return nil, err
	}
	result, err := s.service.Suggest(ctx, request, visibility)
	return result, searchError(err)
}
func (s *searchServer) GetDocument(ctx context.Context, request *searchv1.GetDocumentRequest) (*searchv1.GetDocumentResponse, error) {
	visibility, err := callerVisibility(ctx, request.GetTenantId(), s.access)
	if err != nil {
		return nil, err
	}
	document, err := s.service.Get(ctx, request.GetTenantId(), request.GetId(), visibility)
	if err != nil {
		return nil, searchError(err)
	}
	return &searchv1.GetDocumentResponse{Document: document}, nil
}
func (s *searchServer) BatchUpsertDocuments(ctx context.Context, request *searchv1.BatchUpsertDocumentsRequest) (*searchv1.BatchUpsertDocumentsResponse, error) {
	if err := requireIndexer(ctx); err != nil {
		return nil, err
	}
	if err := s.service.BatchUpsert(ctx, request.GetDocuments()); err != nil {
		return nil, searchError(err)
	}
	return &searchv1.BatchUpsertDocumentsResponse{Accepted: uint32(len(request.GetDocuments()))}, nil
}
func (s *searchServer) BatchDeleteDocuments(ctx context.Context, request *searchv1.BatchDeleteDocumentsRequest) (*searchv1.BatchDeleteDocumentsResponse, error) {
	if err := requireIndexer(ctx); err != nil {
		return nil, err
	}
	if err := s.service.BatchDelete(ctx, request.GetDocuments()); err != nil {
		return nil, searchError(err)
	}
	return &searchv1.BatchDeleteDocumentsResponse{Accepted: uint32(len(request.GetDocuments()))}, nil
}

func requireIndexer(ctx context.Context) error {
	identity, ok := platformprincipal.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "authenticated service principal is required")
	}
	if identity.Type != platformprincipal.TypeServiceAccount && identity.Type != platformprincipal.TypeSystem {
		return status.Error(codes.PermissionDenied, "service principal is required for indexing")
	}
	return nil
}

func callerVisibility(ctx context.Context, tenantID string, access *searchauth.Resolver) ([]string, error) {
	if access != nil {
		values, err := access.VisibilityTokens(ctx, tenantID)
		if err != nil {
			if errors.Is(err, searchauth.ErrForbidden) {
				return nil, status.Error(codes.PermissionDenied, "tenant access denied")
			}
			return nil, status.Error(codes.Unavailable, "authorization unavailable")
		}
		return values, nil
	}
	value, ok := platformprincipal.FromContext(ctx)
	if !ok || value.ID == "" {
		return nil, nil
	}
	if value.TenantID != "" && value.TenantID != tenantID {
		return nil, status.Error(codes.PermissionDenied, "tenant access denied")
	}
	result := []string{"user:" + value.ID}
	if value.MembershipID != "" {
		result = append(result, "membership:"+value.MembershipID)
	}
	return result, nil
}
func searchError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, searchapp.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, searchapp.ErrNotFound):
		return status.Error(codes.NotFound, "document not found")
	case errors.Is(err, searchapp.ErrForbidden):
		return status.Error(codes.PermissionDenied, "application access denied")
	case errors.Is(err, searchapp.ErrUnavailable):
		return status.Error(codes.Unavailable, "search unavailable")
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}

type healthServer struct {
	grpc_health_v1.UnimplementedHealthServer
	health *apphealth.Service
}

func (s *healthServer) Check(ctx context.Context, _ *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	_, ready := s.health.Ready(ctx)
	serving := grpc_health_v1.HealthCheckResponse_NOT_SERVING
	if ready {
		serving = grpc_health_v1.HealthCheckResponse_SERVING
	}
	return &grpc_health_v1.HealthCheckResponse{Status: serving}, nil
}
func (s *healthServer) List(context.Context, *grpc_health_v1.HealthListRequest) (*grpc_health_v1.HealthListResponse, error) {
	return &grpc_health_v1.HealthListResponse{Statuses: map[string]*grpc_health_v1.HealthCheckResponse{"": {Status: grpc_health_v1.HealthCheckResponse_SERVING}}}, nil
}

func requestIDInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	id := ""
	if values := metadata.ValueFromIncomingContext(ctx, "x-request-id"); len(values) > 0 && requestid.Valid(values[0]) {
		id = values[0]
	}
	if id == "" {
		id = requestid.Generate()
	}
	header := metadata.Pairs("x-request-id", id)
	_ = grpc.SetHeader(ctx, header)
	return handler(requestid.WithContext(ctx, id), req)
}
func idempotencyInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	values := metadata.ValueFromIncomingContext(ctx, "idempotency-key")
	if len(values) == 0 {
		return handler(ctx, req)
	}
	if !idempotency.Valid(values[0]) {
		return nil, status.Error(codes.InvalidArgument, "invalid idempotency-key")
	}
	return handler(idempotency.WithContext(ctx, values[0]), req)
}
func environmentInterceptor(profile string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(environment.WithContext(ctx, profile), req)
	}
}
func authInterceptor(service *auth.Service, cfg config.Auth) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		authCtx, err := authenticateGRPC(ctx, info.FullMethod, service, cfg)
		if err != nil {
			return nil, err
		}
		return handler(authCtx, req)
	}
}

func authenticateGRPC(ctx context.Context, method string, service *auth.Service, cfg config.Auth) (context.Context, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if cfg.PSK.Enabled && auth.MatchesAny(method, cfg.PSK.GRPCMethods) {
		if len(values) == 0 || !auth.VerifyPSK(values[0], cfg.PSK.Key) {
			return nil, status.Error(codes.Unauthenticated, "missing or invalid PSK")
		}
		return platformprincipal.WithContext(ctx, platformprincipal.Principal{ID: "search-service:psk", Type: platformprincipal.TypeServiceAccount}), nil
	}
	if auth.MatchesAny(method, cfg.SkipGRPCMethods) {
		return ctx, nil
	}
	if len(values) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing bearer token")
	}
	scheme, raw, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return nil, status.Error(codes.Unauthenticated, "invalid bearer token")
	}
	identity, err := service.Verify(ctx, raw)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
	}
	return platformprincipal.WithContext(ctx, identity), nil
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextServerStream) Context() context.Context { return s.ctx }

func environmentStreamInterceptor(profile string) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &contextServerStream{ServerStream: stream, ctx: environment.WithContext(stream.Context(), profile)})
	}
}

func requestIDStreamInterceptor(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := stream.Context()
	id := ""
	if values := metadata.ValueFromIncomingContext(ctx, "x-request-id"); len(values) > 0 && requestid.Valid(values[0]) {
		id = values[0]
	}
	if id == "" {
		id = requestid.Generate()
	}
	if err := stream.SetHeader(metadata.Pairs("x-request-id", id)); err != nil {
		return status.Error(codes.Internal, "set request metadata")
	}
	return handler(srv, &contextServerStream{ServerStream: stream, ctx: requestid.WithContext(ctx, id)})
}

func idempotencyStreamInterceptor(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	values := metadata.ValueFromIncomingContext(stream.Context(), "idempotency-key")
	if len(values) == 0 {
		return handler(srv, stream)
	}
	if !idempotency.Valid(values[0]) {
		return status.Error(codes.InvalidArgument, "invalid idempotency-key")
	}
	return handler(srv, &contextServerStream{ServerStream: stream, ctx: idempotency.WithContext(stream.Context(), values[0])})
}

func authStreamInterceptor(service *auth.Service, cfg config.Auth) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := authenticateGRPC(stream.Context(), info.FullMethod, service, cfg)
		if err != nil {
			return err
		}
		return handler(srv, &contextServerStream{ServerStream: stream, ctx: ctx})
	}
}

func recoveryStreamInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(stream.Context(), "grpc stream panic recovered", "method", info.FullMethod, "panic", recovered)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(srv, stream)
	}
}

func metricsStreamInterceptor(metrics *observability.Metrics, logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		started := time.Now()
		err := handler(srv, stream)
		code := status.Code(err)
		if metrics.Enabled() {
			metrics.GRPCRequests.WithLabelValues(info.FullMethod, code.String()).Inc()
			metrics.GRPCDuration.WithLabelValues(info.FullMethod).Observe(time.Since(started).Seconds())
		}
		requestID, _ := requestid.FromContext(stream.Context())
		logger.InfoContext(stream.Context(), "grpc stream", "request_id", requestID, "method", info.FullMethod, "code", code.String(), "duration", time.Since(started))
		return err
	}
}

func recoveryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (response any, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(ctx, "grpc panic recovered", "method", info.FullMethod, "panic", recovered)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}
func metricsInterceptor(metrics *observability.Metrics, logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		started := time.Now()
		response, err := handler(ctx, req)
		code := status.Code(err)
		if metrics.Enabled() {
			metrics.GRPCRequests.WithLabelValues(info.FullMethod, code.String()).Inc()
			metrics.GRPCDuration.WithLabelValues(info.FullMethod).Observe(time.Since(started).Seconds())
		}
		span := trace.SpanFromContext(ctx).SpanContext()
		requestID, _ := requestid.FromContext(ctx)
		logger.InfoContext(ctx, "grpc request", "request_id", requestID, "trace_id", span.TraceID().String(), "span_id", span.SpanID().String(), "method", info.FullMethod, "code", code.String(), "duration", time.Since(started))
		return response, err
	}
}

func serverCredentials(cfg config.GRPCTLS) (credentials.TransportCredentials, error) {
	certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load grpc certificate: %w", err)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	if cfg.ClientCAFile != "" {
		pem, err := os.ReadFile(cfg.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read grpc client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("parse grpc client CA")
		}
		tlsConfig.ClientCAs = pool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return credentials.NewTLS(tlsConfig), nil
}

var Module = fx.Module("grpc", fx.Provide(NewServer), fx.Invoke(func(*Server) {}))
