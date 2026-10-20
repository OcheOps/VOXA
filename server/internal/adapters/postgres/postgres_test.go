package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/adapters/postgres"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
	"github.com/OcheOps/voxa/server/internal/repository/repositorytest"
	"github.com/OcheOps/voxa/server/migrations"
)

const testDatabaseURLEnv = "VOXA_TEST_DATABASE_URL"

// isolatedPool returns a pool whose search_path is a fresh, empty schema that
// is dropped when the test ends. Integration tests skip without a test URL.
func isolatedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv(testDatabaseURLEnv)
	if databaseURL == "" {
		t.Skipf("%s not set; skipping PostgreSQL integration test", testDatabaseURLEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := postgres.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	schema := "voxa_test_" + hex.EncodeToString(suffix[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
		admin.Close()
	})
	return pool
}

// migratedPool is an isolated pool with the real schema and the contract
// workspaces bootstrapped.
func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := isolatedPool(t)
	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, workspace := range []domain.WorkspaceID{repositorytest.WorkspaceA, repositorytest.WorkspaceB} {
		if _, err := postgres.EnsureWorkspace(ctx, pool, workspace); err != nil {
			t.Fatalf("ensure workspace: %v", err)
		}
	}
	return pool
}

func TestMeetingRepositoryContract(t *testing.T) {
	repositorytest.MeetingRepositoryContract(t, func(t *testing.T) repository.MeetingRepository {
		return postgres.NewMeetingRepository(migratedPool(t))
	})
}

func TestConfigurationRepositoryContract(t *testing.T) {
	defaults := application.DefaultConfiguration()
	repositorytest.ConfigurationRepositoryContract(t, defaults, func(t *testing.T) repository.ConfigurationRepository {
		return postgres.NewConfigurationRepository(migratedPool(t), defaults)
	})
}

func TestMeetingCreateRejectsUnknownWorkspace(t *testing.T) {
	repo := postgres.NewMeetingRepository(migratedPool(t))
	err := repo.Create(context.Background(), domain.Meeting{ID: "mtg_1", WorkspaceID: "workspace-unknown", Title: "t", Status: domain.MeetingCreated, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	if err == nil || errors.Is(err, repository.ErrConflict) || strings.Contains(err.Error(), "workspace-unknown") {
		t.Fatalf("err = %v, want opaque non-conflict error", err)
	}
}

func TestEnsureWorkspaceIsIdempotent(t *testing.T) {
	pool := isolatedPool(t)
	ctx := context.Background()
	if _, err := postgres.Migrate(ctx, pool, migrations.Files); err != nil {
		t.Fatal(err)
	}
	created, err := postgres.EnsureWorkspace(ctx, pool, "workspace-dev")
	if err != nil || !created {
		t.Fatalf("first ensure created=%v err=%v", created, err)
	}
	created, err = postgres.EnsureWorkspace(ctx, pool, "workspace-dev")
	if err != nil || created {
		t.Fatalf("second ensure created=%v err=%v", created, err)
	}
}

func TestServiceTimestampsSurviveRoundTrip(t *testing.T) {
	pool := migratedPool(t)
	service := application.NewMeetingService(postgres.NewMeetingRepository(pool))
	ctx := context.Background()
	created, err := service.Create(ctx, repositorytest.WorkspaceA, "Planning")
	if err != nil {
		t.Fatal(err)
	}
	// A fresh repository on the same database simulates a restart.
	reloaded, err := application.NewMeetingService(postgres.NewMeetingRepository(pool)).Get(ctx, repositorytest.WorkspaceA, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.CreatedAt.Equal(created.CreatedAt) || !reloaded.UpdatedAt.Equal(created.UpdatedAt) || reloaded.Title != created.Title {
		t.Fatalf("reloaded %#v, created %#v", reloaded, created)
	}
	page, err := service.List(ctx, repositorytest.WorkspaceA, "", 1)
	if err != nil || len(page.Items) != 1 || page.HasMore {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if _, err := service.List(ctx, repositorytest.WorkspaceA, "not-a-cursor", 1); err == nil {
		t.Fatal("malformed cursor accepted")
	}
}

func appliedVersions(t *testing.T, pool *pgxpool.Pool) []int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatal(err)
	}
	return versions
}

func tableExists(t *testing.T, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestMigrationsApplyToEmptyDatabaseAndRerunIsIdempotent(t *testing.T) {
	pool := isolatedPool(t)
	ctx := context.Background()

	before, err := postgres.MigrationState(ctx, pool, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 || before[0].Applied || tableExists(t, pool, "schema_migrations") {
		t.Fatalf("state before migrate = %#v", before)
	}

	applied, err := postgres.Migrate(ctx, pool, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(before) {
		t.Fatalf("applied %d migrations, want %d", len(applied), len(before))
	}
	for _, table := range []string{"workspaces", "meetings", "workspace_configuration", "workspace_credentials", "users", "workspace_memberships", "deployment_state", "recording_sessions", "media_assets", "processing_jobs", "transcripts", "transcript_segments"} {
		if !tableExists(t, pool, table) {
			t.Fatalf("table %s missing", table)
		}
	}

	// Byte-wise ID ordering must not depend on the database locale; some
	// builds (e.g. musl) sort libc collations byte-wise anyway, so check the
	// declared collation rather than relying on observed ordering.
	var collations []string
	rows, err := pool.Query(ctx, `SELECT table_name || '.' || column_name || '=' || coalesce(collation_name, '')
FROM information_schema.columns
WHERE table_schema = current_schema() AND column_name IN ('id', 'workspace_id')
ORDER BY 1`)
	if err == nil {
		collations, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(collations, " "); got != "media_assets.id=C media_assets.workspace_id=C meetings.id=C meetings.workspace_id=C processing_jobs.id=C processing_jobs.workspace_id=C recording_sessions.id=C recording_sessions.workspace_id=C transcripts.id=C transcripts.workspace_id=C users.id=C workspace_configuration.workspace_id=C workspace_credentials.workspace_id=C workspace_memberships.workspace_id=C workspaces.id=C" {
		t.Fatalf("identifier collations = %s", got)
	}

	again, err := postgres.Migrate(ctx, pool, migrations.Files)
	if err != nil || len(again) != 0 {
		t.Fatalf("rerun applied=%d err=%v", len(again), err)
	}
	state, err := postgres.MigrationState(ctx, pool, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range state {
		if !status.Applied || status.AppliedAt == nil {
			t.Fatalf("status %#v not applied", status)
		}
	}
	if got := appliedVersions(t, pool); len(got) != len(state) || got[0] != 1 {
		t.Fatalf("tracked versions = %v", got)
	}
}

func TestMigrationFailureStopsAndRollsBackFailingMigration(t *testing.T) {
	pool := isolatedPool(t)
	files := fstest.MapFS{
		"0001_first.sql":  {Data: []byte(`CREATE TABLE first_table (id int);`)},
		"0002_broken.sql": {Data: []byte(`CREATE TABLE broken_table (id int); SELECT * FROM table_that_does_not_exist;`)},
		"0003_later.sql":  {Data: []byte(`CREATE TABLE later_table (id int);`)},
	}
	applied, err := postgres.Migrate(context.Background(), pool, files)
	if err == nil {
		t.Fatal("broken migration succeeded")
	}
	if len(applied) != 1 || applied[0].Version != 1 {
		t.Fatalf("applied = %#v", applied)
	}
	if !tableExists(t, pool, "first_table") || tableExists(t, pool, "broken_table") || tableExists(t, pool, "later_table") {
		t.Fatal("partial migration state leaked")
	}
	if got := appliedVersions(t, pool); len(got) != 1 || got[0] != 1 {
		t.Fatalf("tracked versions = %v", got)
	}
}

func TestMigrationRejectsModifiedOrUnknownAppliedMigrations(t *testing.T) {
	pool := isolatedPool(t)
	ctx := context.Background()
	original := fstest.MapFS{
		"0001_first.sql":  {Data: []byte(`CREATE TABLE first_table (id int);`)},
		"0002_second.sql": {Data: []byte(`CREATE TABLE second_table (id int);`)},
	}
	if _, err := postgres.Migrate(ctx, pool, original); err != nil {
		t.Fatal(err)
	}
	modified := fstest.MapFS{
		"0001_first.sql":  {Data: []byte(`CREATE TABLE first_table (id bigint);`)},
		"0002_second.sql": original["0002_second.sql"],
	}
	if _, err := postgres.Migrate(ctx, pool, modified); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("modified migration err = %v", err)
	}
	older := fstest.MapFS{"0001_first.sql": original["0001_first.sql"]}
	if _, err := postgres.Migrate(ctx, pool, older); err == nil || !strings.Contains(err.Error(), "does not know") {
		t.Fatalf("unknown applied migration err = %v", err)
	}
	if _, err := postgres.MigrationState(ctx, pool, older); err == nil {
		t.Fatal("state accepted newer schema")
	}
}

func TestConcurrentMigrationRunsApplyOnce(t *testing.T) {
	pool := isolatedPool(t)
	const runners = 4
	var wait sync.WaitGroup
	var mu sync.Mutex
	total := 0
	wait.Add(runners)
	for range runners {
		go func() {
			defer wait.Done()
			applied, err := postgres.Migrate(context.Background(), pool, migrations.Files)
			if err != nil {
				t.Errorf("migrate: %v", err)
				return
			}
			mu.Lock()
			total += len(applied)
			mu.Unlock()
		}()
	}
	wait.Wait()
	state, err := postgres.MigrationState(context.Background(), pool, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(state) {
		t.Fatalf("applied %d migrations across runners, want %d", total, len(state))
	}
}

func TestDatabaseWithMeetingsAndConfigurationUpgradesToCredentials(t *testing.T) {
	pool := isolatedPool(t)
	ctx := context.Background()
	initial, err := fs.ReadFile(migrations.Files, "0001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	// A database created by the previous release: only 0001 applied, with data.
	if _, err := postgres.Migrate(ctx, pool, fstest.MapFS{"0001_initial.sql": {Data: initial}}); err != nil {
		t.Fatal(err)
	}
	if _, err := postgres.EnsureWorkspace(ctx, pool, repositorytest.WorkspaceA); err != nil {
		t.Fatal(err)
	}
	meetings := postgres.NewMeetingRepository(pool)
	created := domain.Meeting{ID: "mtg_existing", WorkspaceID: repositorytest.WorkspaceA, Title: "Before upgrade", Status: domain.MeetingCreated, CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	created.UpdatedAt = created.CreatedAt
	if err := meetings.Create(ctx, created); err != nil {
		t.Fatal(err)
	}
	configurations := postgres.NewConfigurationRepository(pool, application.DefaultConfiguration())
	if _, err := configurations.Update(ctx, repositorytest.WorkspaceA, func(c domain.Configuration) (domain.Configuration, error) {
		c.Summary.Model = "kept-model"
		return c, nil
	}); err != nil {
		t.Fatal(err)
	}
	if tableExists(t, pool, "workspace_credentials") {
		t.Fatal("credential table exists before upgrade")
	}

	applied, err := postgres.Migrate(ctx, pool, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(mustLoad(t))-1 || applied[0].Version != 2 {
		t.Fatalf("upgrade applied %#v, want every migration after 0001", applied)
	}
	if got, err := meetings.Get(ctx, repositorytest.WorkspaceA, created.ID); err != nil || got.Title != created.Title {
		t.Fatalf("meeting after upgrade = %#v err=%v", got, err)
	}
	if got, err := configurations.Get(ctx, repositorytest.WorkspaceA); err != nil || got.Summary.Model != "kept-model" {
		t.Fatalf("configuration after upgrade = %#v err=%v", got, err)
	}
	if !tableExists(t, pool, "workspace_credentials") {
		t.Fatal("credential table missing after upgrade")
	}
	if again, err := postgres.Migrate(ctx, pool, migrations.Files); err != nil || len(again) != 0 {
		t.Fatalf("rerun applied=%d err=%v", len(again), err)
	}
}

func mustLoad(t *testing.T) []postgres.Migration {
	t.Helper()
	loaded, err := postgres.LoadMigrations(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}
