package postgres_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/adapters/postgres"
	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/auth"
	"github.com/OcheOps/voxa/server/internal/auth/authtest"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
	"github.com/OcheOps/voxa/server/internal/repository/repositorytest"
	"github.com/OcheOps/voxa/server/migrations"
)

// schemaOnlyPool is migrated but, unlike migratedPool, has no workspaces:
// an empty, uninitialized deployment.
func schemaOnlyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := isolatedPool(t)
	if _, err := postgres.Migrate(context.Background(), pool, migrations.Files); err != nil {
		t.Fatal(err)
	}
	return pool
}

func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIdentityRepositoryContract(t *testing.T) {
	repositorytest.IdentityRepositoryContract(t, func(t *testing.T) repository.IdentityRepository {
		return postgres.NewIdentityRepository(schemaOnlyPool(t))
	})
}

func TestConcurrentFirstLoginsCreateOneOwnerAndWorkspace(t *testing.T) {
	pool := schemaOnlyPool(t)
	resolver := application.NewMembershipResolver(postgres.NewIdentityRepository(pool), application.MembershipResolverOptions{})
	const callers = 10
	var wait sync.WaitGroup
	var mu sync.Mutex
	granted := 0
	for i := range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := resolver.Resolve(context.Background(), domain.Identity{Issuer: repositorytest.TestIssuer, Subject: fmt.Sprintf("subject-%d", i)})
			if err == nil {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wait.Wait()
	if granted != 1 {
		t.Fatalf("%d identities gained access, want 1", granted)
	}
	for table, want := range map[string]int{"users": 1, "workspaces": 1, "workspace_memberships": 1, "deployment_state": 1} {
		if got := count(t, pool, table); got != want {
			t.Fatalf("%s rows = %d, want %d", table, got, want)
		}
	}
	var role string
	if err := pool.QueryRow(context.Background(), "SELECT role FROM workspace_memberships").Scan(&role); err != nil || role != "owner" {
		t.Fatalf("role=%q err=%v", role, err)
	}
}

func TestConcurrentLoginsBySameFirstIdentity(t *testing.T) {
	pool := schemaOnlyPool(t)
	resolver := application.NewMembershipResolver(postgres.NewIdentityRepository(pool), application.MembershipResolverOptions{})
	identity := domain.Identity{Issuer: repositorytest.TestIssuer, Subject: "alice"}
	var wait sync.WaitGroup
	users := make(chan domain.UserID, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			principal, err := resolver.Resolve(context.Background(), identity)
			if err != nil {
				t.Errorf("resolve: %v", err)
				return
			}
			users <- principal.User.ID
		}()
	}
	wait.Wait()
	close(users)
	seen := map[domain.UserID]bool{}
	for id := range users {
		seen[id] = true
	}
	if len(seen) != 1 || count(t, pool, "users") != 1 {
		t.Fatalf("user ids = %v", seen)
	}
}

func TestBootstrapStateSurvivesRestart(t *testing.T) {
	pool := schemaOnlyPool(t)
	ctx := context.Background()
	alice := domain.Identity{Issuer: repositorytest.TestIssuer, Subject: "alice"}
	first, err := application.NewMembershipResolver(postgres.NewIdentityRepository(pool), application.MembershipResolverOptions{}).Resolve(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	restarted := application.NewMembershipResolver(postgres.NewIdentityRepository(pool), application.MembershipResolverOptions{})
	again, err := restarted.Resolve(ctx, alice)
	if err != nil || again.User.ID != first.User.ID || again.Workspace.ID != first.Workspace.ID || again.Role != domain.RoleOwner {
		t.Fatalf("after restart principal=%#v err=%v", again, err)
	}
	if _, err := restarted.Resolve(ctx, domain.Identity{Issuer: repositorytest.TestIssuer, Subject: "bob"}); err == nil {
		t.Fatal("second identity gained access after restart")
	}
}

func TestBootstrapAdoptsExistingDevelopmentWorkspace(t *testing.T) {
	pool := schemaOnlyPool(t)
	ctx := context.Background()
	// Data created earlier in development mode.
	if _, err := postgres.EnsureWorkspace(ctx, pool, "workspace-dev"); err != nil {
		t.Fatal(err)
	}
	meetings := application.NewMeetingService(postgres.NewMeetingRepository(pool))
	existing, err := meetings.Create(ctx, "workspace-dev", "Before OIDC")
	if err != nil {
		t.Fatal(err)
	}
	resolver := application.NewMembershipResolver(postgres.NewIdentityRepository(pool), application.MembershipResolverOptions{BootstrapWorkspaceID: "workspace-dev"})
	principal, err := resolver.Resolve(ctx, domain.Identity{Issuer: repositorytest.TestIssuer, Subject: "alice"})
	if err != nil || principal.Workspace.ID != "workspace-dev" || principal.Role != domain.RoleOwner {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	if got, err := meetings.Get(ctx, principal.Workspace.ID, existing.ID); err != nil || got.Title != "Before OIDC" {
		t.Fatalf("existing meeting lost: %#v err=%v", got, err)
	}
	if count(t, pool, "workspaces") != 1 {
		t.Fatal("adoption created a second workspace")
	}
}

func TestDeletingUserRemovesOnlyMemberships(t *testing.T) {
	pool := schemaOnlyPool(t)
	ctx := context.Background()
	principal, err := application.NewMembershipResolver(postgres.NewIdentityRepository(pool), application.MembershipResolverOptions{}).
		Resolve(ctx, domain.Identity{Issuer: repositorytest.TestIssuer, Subject: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewMeetingService(postgres.NewMeetingRepository(pool)).Create(ctx, principal.Workspace.ID, "Kept"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", string(principal.User.ID)); err != nil {
		t.Fatal(err)
	}
	if count(t, pool, "workspace_memberships") != 0 || count(t, pool, "meetings") != 1 || count(t, pool, "workspaces") != 1 || count(t, pool, "deployment_state") != 1 {
		t.Fatal("user deletion cascaded beyond memberships")
	}
}

func TestIdentitySchemaConstraints(t *testing.T) {
	pool := schemaOnlyPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, issuer, subject, created_at, updated_at) VALUES ('usr_1', 'https://i', 's', now(), now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspaces (id) VALUES ('wsp_1')`); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"duplicate identity": `INSERT INTO users (id, issuer, subject, created_at, updated_at) VALUES ('usr_2', 'https://i', 's', now(), now())`,
		"unknown role":       `INSERT INTO workspace_memberships (workspace_id, user_id, role) VALUES ('wsp_1', 'usr_1', 'admin')`,
		"second bootstrap":   `INSERT INTO deployment_state (bootstrap_user_id, bootstrap_workspace_id) VALUES ('a', 'b'), ('c', 'd')`,
		"non-singleton":      `INSERT INTO deployment_state (singleton, bootstrap_user_id, bootstrap_workspace_id) VALUES (false, 'a', 'b')`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace_memberships (workspace_id, user_id, role) VALUES ('wsp_1', 'usr_1', 'member')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace_memberships (workspace_id, user_id, role) VALUES ('wsp_1', 'usr_1', 'owner')`); err == nil {
		t.Error("duplicate membership accepted")
	}
}

func TestDatabaseAtCredentialsVersionUpgradesToIdentity(t *testing.T) {
	pool := isolatedPool(t)
	ctx := context.Background()
	previous := fstest.MapFS{}
	for _, name := range []string{"0001_initial.sql", "0002_workspace_credentials.sql"} {
		data, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			t.Fatal(err)
		}
		previous[name] = &fstest.MapFile{Data: data}
	}
	if _, err := postgres.Migrate(ctx, pool, previous); err != nil {
		t.Fatal(err)
	}
	if _, err := postgres.EnsureWorkspace(ctx, pool, "workspace-dev"); err != nil {
		t.Fatal(err)
	}
	applied, err := postgres.Migrate(ctx, pool, migrations.Files)
	if err != nil || len(applied) != 1 || applied[0].Version != 3 {
		t.Fatalf("applied=%#v err=%v", applied, err)
	}
	var name string
	if err := pool.QueryRow(ctx, "SELECT name FROM workspaces WHERE id = 'workspace-dev'").Scan(&name); err != nil || name != application.DefaultWorkspaceName {
		t.Fatalf("existing workspace name=%q err=%v", name, err)
	}
	if initialized, err := postgres.NewIdentityRepository(pool).IsInitialized(ctx); err != nil || initialized {
		t.Fatalf("upgraded deployment initialized=%v err=%v", initialized, err)
	}
}

func oidcServer(t *testing.T, pool *pgxpool.Pool, issuer *authtest.Issuer, logs *bytes.Buffer) http.Handler {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authenticator, err := auth.NewOIDCAuthenticator(ctx, auth.OIDCConfig{Issuer: issuer.URL, Audience: authtest.Audience})
	if err != nil {
		t.Fatal(err)
	}
	credentials := postgres.NewCredentialRepository(pool, credentialCipher(t, "v1", map[string][]byte{"v1": testKey(t)}))
	return api.NewHandler(
		application.NewMeetingService(postgres.NewMeetingRepository(pool)),
		application.NewConfigurationService(postgres.NewConfigurationRepository(pool, application.DefaultConfiguration()), credentials),
		application.NewCredentialService(credentials),
		authenticator,
		application.NewMembershipResolver(postgres.NewIdentityRepository(pool), application.MembershipResolverOptions{}),
		"test", slog.New(slog.NewJSONHandler(logs, nil)),
	)
}

func authorized(handler http.Handler, method, target, body, token string) (int, string) {
	r := newRequest(method, target, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := serve(handler, r)
	return recorder.Code, recorder.Body.String()
}

func TestOIDCHTTPFlowSurvivesRestart(t *testing.T) {
	pool := schemaOnlyPool(t)
	issuer := authtest.NewIssuer(t)
	var logs bytes.Buffer
	alice := issuer.TokenFor(t, "alice", map[string]any{"email": "alice@example.test", "name": "Alice"})

	first := oidcServer(t, pool, issuer, &logs)
	if code, body := authorized(first, http.MethodGet, "/api/v1/meetings", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", code, body)
	}
	code, body := authorized(first, http.MethodPost, "/api/v1/meetings", `{"title":"Persisted"}`, alice)
	if code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", code, body)
	}

	restarted := oidcServer(t, pool, issuer, &logs)
	if code, body := authorized(restarted, http.MethodGet, "/api/v1/meetings", "", alice); code != http.StatusOK || !strings.Contains(body, "Persisted") {
		t.Fatalf("after restart status=%d body=%s", code, body)
	}
	if code, body := authorized(restarted, http.MethodGet, "/api/v1/me", "", alice); code != http.StatusOK || !strings.Contains(body, `"role":"owner"`) || !strings.Contains(body, `"email":"alice@example.test"`) {
		t.Fatalf("me status=%d body=%s", code, body)
	}
	bob := issuer.TokenFor(t, "bob", nil)
	if code, body := authorized(restarted, http.MethodGet, "/api/v1/meetings", "", bob); code != http.StatusForbidden || strings.Contains(body, "Persisted") {
		t.Fatalf("bob status=%d body=%s", code, body)
	}
	if count(t, pool, "users") != 1 {
		t.Fatal("unknown identity was persisted")
	}
	for _, leaked := range []string{alice, bob, "alice@example.test"} {
		if strings.Contains(logs.String(), leaked) {
			t.Fatal("logs leaked token or email")
		}
	}
}
