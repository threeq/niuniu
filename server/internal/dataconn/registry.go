package dataconn

import (
	"fmt"
	"sync"
)

type Registry struct{ conns map[SourceKind]Connector }

func NewRegistry() *Registry {
	return &Registry{conns: map[SourceKind]Connector{
		KindMySQL:       NewMySQLConnector(),
		KindPostgres:    NewPostgresConnector(),
		KindClickHouse:  NewClickHouseConnector(),
		KindMSSQL:       NewMSSQLConnector(),
		KindMariaDB:     NewMySQLCompatConnector(KindMariaDB),
		KindTiDB:        NewMySQLCompatConnector(KindTiDB),
		KindOceanBase:   NewMySQLCompatConnector(KindOceanBase),
		KindStarRocks:   NewMySQLCompatConnector(KindStarRocks),
		KindDoris:       NewMySQLCompatConnector(KindDoris),
		KindCockroachDB: NewPostgresCompatConnector(KindCockroachDB),
		KindGreenplum:   NewPostgresCompatConnector(KindGreenplum),
		KindRedshift:    NewPostgresCompatConnector(KindRedshift),
		KindOpenGauss:   NewPostgresCompatConnector(KindOpenGauss),
		KindPolarDBPG:   NewPostgresCompatConnector(KindPolarDBPG),
		KindYugabyte:    NewPostgresCompatConnector(KindYugabyte),
		KindTrino:       NewTrinoConnector(),

		// NoSQL kinds (Epic #345 Wave3): all four connectors are real now
		// (#355 trino above, #356 mongo, #357 redis, #358 elasticsearch);
		// the Wave2 #354 stubs are gone.
		KindElasticsearch: NewElasticsearchConnector(),
		KindRedis:         NewRedisConnector(),
		KindMongo:         NewMongoConnector(),

		// Generic HTTP/JSON REST source (its own FamilyHTTP; path-prefix scope).
		KindHTTP: NewHTTPConnector(),
	}}
}

func (r *Registry) Get(kind SourceKind) (Connector, error) {
	c, ok := r.conns[kind]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, kind)
	}
	return c, nil
}

// Register installs (or replaces) the connector for a kind. M2 uses it to add
// real Redis/ClickHouse/Mongo connectors; service tests use it to inject a fake
// connector (e.g. an in-memory SQLite-backed one) so the data-proxy gate can be
// exercised without a live database.
func (r *Registry) Register(kind SourceKind, c Connector) {
	r.conns[kind] = c
}

// Pool is a placeholder for future connection reuse. M1 connectors open a
// short-lived *sql.DB per Execute (correct + simplest); the Pool exists so the
// service layer can call Evict(sourceID) on credential change/delete without a
// later refactor. M2 wires real pooling here.
type Pool struct {
	mu sync.Mutex
}

func NewPool() *Pool           { return &Pool{} }
func (p *Pool) Evict(id int64) {}
