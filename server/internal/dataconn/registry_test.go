package dataconn

import (
	"context"
	"testing"
)

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()

	// All 15 SQL kinds plus the four NoSQL/federation kinds (Epic #345)
	// must be registered.
	kinds := []SourceKind{
		KindMySQL, KindPostgres, KindClickHouse, KindMSSQL,
		KindMariaDB, KindTiDB, KindOceanBase, KindStarRocks, KindDoris,
		KindCockroachDB, KindGreenplum, KindRedshift,
		KindOpenGauss, KindPolarDBPG, KindYugabyte,
		KindTrino, KindElasticsearch, KindRedis, KindMongo, KindHTTP,
	}
	for _, k := range kinds {
		c, err := r.Get(k)
		if err != nil || c.Kind() != k {
			t.Errorf("kind %s: conn=%v err=%v", k, c, err)
		}
	}

	if _, err := r.Get(SourceKind("oracle")); err == nil {
		t.Error("unregistered kind should return an error")
	}
}

func TestPoolEvictNoPanic(t *testing.T) {
	p := NewPool()
	p.Evict(42) // evicting absent id must be safe
	_ = context.Background()
}
