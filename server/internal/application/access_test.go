package application_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
)

const issuer = "https://issuer.example.test"

func identity(subject string) domain.Identity {
	return domain.Identity{Issuer: issuer, Subject: subject}
}

func assertAccessError(t *testing.T, err error, code application.ErrorCode, category string) {
	t.Helper()
	var appErr *application.Error
	if !errors.As(err, &appErr) || appErr.Code != code || appErr.Category != category {
		t.Fatalf("err = %#v, want %s/%s", err, code, category)
	}
}

func TestFirstIdentityBootstrapsAndLaterIdentitiesDoNotJoin(t *testing.T) {
	repo := memory.NewIdentityRepository()
	resolver := application.NewMembershipResolver(repo, application.MembershipResolverOptions{})
	ctx := context.Background()

	first, err := resolver.Resolve(ctx, identity("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Role != domain.RoleOwner || !strings.HasPrefix(string(first.User.ID), "usr_") || !strings.HasPrefix(string(first.Workspace.ID), "wsp_") || first.Workspace.Name != application.DefaultWorkspaceName {
		t.Fatalf("principal = %#v", first)
	}
	again, err := resolver.Resolve(ctx, identity("alice"))
	if err != nil || again.User.ID != first.User.ID || again.Workspace.ID != first.Workspace.ID {
		t.Fatalf("retry principal=%#v err=%v", again, err)
	}
	_, err = resolver.Resolve(ctx, identity("mallory"))
	assertAccessError(t, err, application.Forbidden, "unknown_identity")
	// Same subject from a different issuer is a different identity.
	_, err = resolver.Resolve(ctx, domain.Identity{Issuer: "https://evil.example.test", Subject: "alice"})
	assertAccessError(t, err, application.Forbidden, "unknown_identity")
}

func TestConcurrentFirstLoginsBootstrapOnce(t *testing.T) {
	repo := memory.NewIdentityRepository()
	resolver := application.NewMembershipResolver(repo, application.MembershipResolverOptions{})
	const callers = 12
	principals := make(chan domain.Principal, callers)
	failures := make(chan error, callers)
	var wait sync.WaitGroup
	for i := range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			principal, err := resolver.Resolve(context.Background(), identity(fmt.Sprintf("subject-%d", i)))
			if err != nil {
				failures <- err
				return
			}
			principals <- principal
		}()
	}
	wait.Wait()
	close(principals)
	close(failures)
	if len(principals) != 1 {
		t.Fatalf("%d identities gained access, want exactly 1", len(principals))
	}
	for err := range failures {
		assertAccessError(t, err, application.Forbidden, "unknown_identity")
	}
}

func TestConcurrentLoginsBySameFirstIdentityShareOneBootstrap(t *testing.T) {
	resolver := application.NewMembershipResolver(memory.NewIdentityRepository(), application.MembershipResolverOptions{})
	const callers = 12
	results := make(chan domain.Principal, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			principal, err := resolver.Resolve(context.Background(), identity("alice"))
			if err != nil {
				t.Errorf("resolve: %v", err)
				return
			}
			results <- principal
		}()
	}
	wait.Wait()
	close(results)
	var first *domain.Principal
	for principal := range results {
		if first == nil {
			first = &principal
		} else if principal.User.ID != first.User.ID || principal.Workspace.ID != first.Workspace.ID {
			t.Fatalf("divergent bootstrap: %#v vs %#v", principal, *first)
		}
	}
}

func TestBootstrapRestrictions(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewIdentityRepository()
	resolver := application.NewMembershipResolver(repo, application.MembershipResolverOptions{BootstrapSubject: "admin-subject", BootstrapWorkspaceID: "workspace-dev"})
	_, err := resolver.Resolve(ctx, identity("someone-else"))
	assertAccessError(t, err, application.Forbidden, "bootstrap_not_permitted")
	if initialized, _ := repo.IsInitialized(ctx); initialized {
		t.Fatal("rejected identity initialized the deployment")
	}
	principal, err := resolver.Resolve(ctx, identity("admin-subject"))
	if err != nil || principal.Workspace.ID != "workspace-dev" || principal.Role != domain.RoleOwner {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
}

func TestMembershipStates(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewIdentityRepository()
	resolver := application.NewMembershipResolver(repo, application.MembershipResolverOptions{})
	owner, err := resolver.Resolve(ctx, identity("owner"))
	if err != nil {
		t.Fatal(err)
	}

	member := domain.User{ID: "usr_member", Issuer: issuer, Subject: "member"}
	repo.AddUser(member)
	_, err = resolver.Resolve(ctx, identity("member"))
	assertAccessError(t, err, application.Forbidden, "not_a_member")

	repo.AddMembership(member.ID, owner.Workspace, domain.RoleMember)
	principal, err := resolver.Resolve(ctx, identity("member"))
	if err != nil || principal.Workspace.ID != owner.Workspace.ID || principal.Role != domain.RoleMember {
		t.Fatalf("member principal=%#v err=%v", principal, err)
	}

	repo.AddMembership(member.ID, domain.Workspace{ID: "wsp_second", Name: "Second"}, domain.RoleMember)
	_, err = resolver.Resolve(ctx, identity("member"))
	assertAccessError(t, err, application.Conflict, "workspace_selection_required")
}

func TestProfileSyncKeepsUserID(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewIdentityRepository()
	resolver := application.NewMembershipResolver(repo, application.MembershipResolverOptions{})
	email := "old@example.test"
	first, err := resolver.Resolve(ctx, domain.Identity{Issuer: issuer, Subject: "alice", Email: &email})
	if err != nil {
		t.Fatal(err)
	}
	newEmail, name := "new@example.test", "Alice"
	second, err := resolver.Resolve(ctx, domain.Identity{Issuer: issuer, Subject: "alice", Email: &newEmail, DisplayName: &name})
	if err != nil || second.User.ID != first.User.ID || *second.User.Email != newEmail || *second.User.DisplayName != name {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	stored, _ := repo.FindUser(ctx, issuer, "alice")
	if *stored.Email != newEmail {
		t.Fatalf("stored email = %v", *stored.Email)
	}
	// Unusable profile hints are dropped rather than stored.
	bad := "line\nbreak@example.test"
	third, err := resolver.Resolve(ctx, domain.Identity{Issuer: issuer, Subject: "alice", Email: &bad})
	if err != nil || third.User.Email != nil {
		t.Fatalf("third=%#v err=%v", third, err)
	}
}

func TestInvalidIdentityIsUnauthorized(t *testing.T) {
	resolver := application.NewMembershipResolver(memory.NewIdentityRepository(), application.MembershipResolverOptions{})
	for _, id := range []domain.Identity{{Issuer: issuer}, {Subject: "s"}, {Issuer: issuer, Subject: strings.Repeat("s", 256)}} {
		_, err := resolver.Resolve(context.Background(), id)
		assertAccessError(t, err, application.Unauthorized, "identity_invalid")
	}
}

func TestDevelopmentResolverIsDeterministic(t *testing.T) {
	principal, err := application.NewDevelopmentResolver("workspace-dev").Resolve(context.Background(), domain.Identity{})
	if err != nil || principal.Workspace.ID != "workspace-dev" || principal.User.ID != application.DevelopmentUserID || principal.Role != domain.RoleOwner {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
}
