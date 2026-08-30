package health

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lihongjie0209/search-service/internal/config"
	searchapp "github.com/lihongjie0209/search-service/internal/search"
	"github.com/redis/go-redis/v9"
)

type Status struct {
	Status       string                `json:"status"`
	Dependencies map[string]Dependency `json:"dependencies,omitempty"`
}
type Dependency struct {
	Status  string `json:"status"`
	Latency string `json:"latency,omitempty"`
}
type Service struct {
	db     *sqlx.DB
	redis  *redis.Client
	cfg    config.Health
	search *searchapp.Service
}
type result struct {
	name       string
	dependency Dependency
	ready      bool
}

func New(db *sqlx.DB, client *redis.Client, searchService *searchapp.Service, cfg config.Config) *Service {
	return &Service{db: db, redis: client, search: searchService, cfg: cfg.Health}
}
func (s *Service) Live() Status { return Status{Status: "up"} }

func (s *Service) Ready(ctx context.Context) (Status, bool) {
	results := make(chan result, 3)
	go s.checkDatabase(ctx, results)
	go s.checkRedis(ctx, results)
	go s.checkOpenSearch(ctx, results)
	status := Status{Status: "ready", Dependencies: make(map[string]Dependency, 3)}
	ready := true
	for range 3 {
		item := <-results
		status.Dependencies[item.name] = item.dependency
		ready = ready && item.ready
	}
	if !ready {
		status.Status = "not_ready"
	}
	return status, ready
}

func (s *Service) checkOpenSearch(parent context.Context, results chan<- result) {
	if s.search == nil {
		results <- result{name: "opensearch", dependency: Dependency{Status: "disabled"}, ready: true}
		return
	}
	ctx, cancel := context.WithTimeout(parent, s.cfg.OpenSearchTimeout)
	defer cancel()
	started := time.Now()
	results <- checkResult("opensearch", started, s.search.Ping(ctx))
}

func (s *Service) checkDatabase(parent context.Context, results chan<- result) {
	if s.db == nil {
		results <- result{name: "database", dependency: Dependency{Status: "disabled"}, ready: true}
		return
	}
	ctx, cancel := context.WithTimeout(parent, s.cfg.DatabaseTimeout)
	defer cancel()
	started := time.Now()
	results <- checkResult("database", started, s.db.PingContext(ctx))
}

func (s *Service) checkRedis(parent context.Context, results chan<- result) {
	if s.redis == nil {
		results <- result{name: "redis", dependency: Dependency{Status: "disabled"}, ready: true}
		return
	}
	ctx, cancel := context.WithTimeout(parent, s.cfg.RedisTimeout)
	defer cancel()
	started := time.Now()
	results <- checkResult("redis", started, s.redis.Ping(ctx).Err())
}

func checkResult(name string, started time.Time, err error) result {
	dependency := Dependency{Status: "up", Latency: time.Since(started).String()}
	if err != nil {
		dependency.Status = "down"
		return result{name: name, dependency: dependency, ready: false}
	}
	return result{name: name, dependency: dependency, ready: true}
}
