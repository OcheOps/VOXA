package postgres_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/OcheOps/voxa/server/internal/adapters/postgres"
	"github.com/OcheOps/voxa/server/migrations"
)

func TestEmbeddedMigrationsLoadInOrder(t *testing.T) {
	loaded, err := postgres.LoadMigrations(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) == 0 || loaded[0].Version != 1 || loaded[0].Name != "initial" || loaded[0].Checksum == "" {
		t.Fatalf("loaded = %#v", loaded)
	}
	for i := 1; i < len(loaded); i++ {
		if loaded[i].Version <= loaded[i-1].Version {
			t.Fatalf("migrations out of order: %d after %d", loaded[i].Version, loaded[i-1].Version)
		}
	}
}

func TestLoadMigrationsSortsAndValidatesNames(t *testing.T) {
	loaded, err := postgres.LoadMigrations(fstest.MapFS{
		"0010_tenth.sql":  {Data: []byte("SELECT 10;")},
		"0002_second.sql": {Data: []byte("SELECT 2;")},
		"README.md":       {Data: []byte("ignored")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].Version != 2 || loaded[1].Version != 10 {
		t.Fatalf("loaded = %#v", loaded)
	}

	cases := map[string]fstest.MapFS{
		"name":      {"init.sql": {Data: []byte("SELECT 1;")}},
		"zero":      {"0000_zero.sql": {Data: []byte("SELECT 1;")}},
		"duplicate": {"0001_a.sql": {Data: []byte("SELECT 1;")}, "0001_b.sql": {Data: []byte("SELECT 1;")}},
	}
	for name, files := range cases {
		if _, err := postgres.LoadMigrations(files); err == nil {
			t.Errorf("%s: invalid migration set accepted", name)
		}
	}
}

func TestConnectDoesNotEchoDatabaseURL(t *testing.T) {
	_, err := postgres.Connect(t.Context(), "postgres://voxa:super-secret@localhost:notaport/voxa")
	if err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("err = %v", err)
	}
}
