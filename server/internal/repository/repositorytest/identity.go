package repositorytest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

const TestIssuer = "https://issuer.example.test"

func testUser(n int) domain.User {
	created := baseTime.Add(time.Duration(n) * time.Second)
	email := fmt.Sprintf("user%d@example.test", n)
	return domain.User{ID: domain.UserID(fmt.Sprintf("usr_%d", n)), Issuer: TestIssuer, Subject: fmt.Sprintf("subject-%d", n), Email: &email, CreatedAt: created, UpdatedAt: created}
}

// IdentityRepositoryContract runs the shared identity/bootstrap semantics.
// newRepository must return an empty, uninitialized repository.
func IdentityRepositoryContract(t *testing.T, newRepository func(t *testing.T) repository.IdentityRepository) {
	ctx := context.Background()

	t.Run("empty deployment", func(t *testing.T) {
		repo := newRepository(t)
		if initialized, err := repo.IsInitialized(ctx); err != nil || initialized {
			t.Fatalf("initialized=%v err=%v", initialized, err)
		}
		if _, err := repo.FindUser(ctx, TestIssuer, "nobody"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("find err = %v", err)
		}
	})

	t.Run("bootstrap creates owner membership once", func(t *testing.T) {
		repo := newRepository(t)
		owner := testUser(1)
		workspace := domain.Workspace{ID: "wsp_default", Name: "Default workspace"}
		if err := repo.Bootstrap(ctx, owner, workspace); err != nil {
			t.Fatal(err)
		}
		found, err := repo.FindUser(ctx, TestIssuer, owner.Subject)
		if err != nil || found.ID != owner.ID || *found.Email != *owner.Email || !found.CreatedAt.Equal(owner.CreatedAt) {
			t.Fatalf("found=%#v err=%v", found, err)
		}
		memberships, err := repo.ListMemberships(ctx, owner.ID)
		if err != nil || len(memberships) != 1 || memberships[0].Workspace != workspace || memberships[0].Role != domain.RoleOwner {
			t.Fatalf("memberships=%#v err=%v", memberships, err)
		}
		if initialized, err := repo.IsInitialized(ctx); err != nil || !initialized {
			t.Fatalf("initialized=%v err=%v", initialized, err)
		}
		second := testUser(2)
		if err := repo.Bootstrap(ctx, second, domain.Workspace{ID: "wsp_other", Name: "Other"}); !errors.Is(err, repository.ErrAlreadyInitialized) {
			t.Fatalf("second bootstrap err = %v", err)
		}
		if _, err := repo.FindUser(ctx, TestIssuer, second.Subject); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("second bootstrap left a user behind: %v", err)
		}
	})

	t.Run("identity is issuer plus subject", func(t *testing.T) {
		repo := newRepository(t)
		owner := testUser(1)
		if err := repo.Bootstrap(ctx, owner, domain.Workspace{ID: "wsp_default", Name: "Default workspace"}); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.FindUser(ctx, "https://other-issuer.example.test", owner.Subject); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("same subject at another issuer matched: %v", err)
		}
	})

	t.Run("profile update keeps identity", func(t *testing.T) {
		repo := newRepository(t)
		owner := testUser(1)
		if err := repo.Bootstrap(ctx, owner, domain.Workspace{ID: "wsp_default", Name: "Default workspace"}); err != nil {
			t.Fatal(err)
		}
		email, name := "renamed@example.test", "Renamed"
		later := owner.UpdatedAt.Add(time.Hour)
		if err := repo.UpdateUserProfile(ctx, owner.ID, &email, &name, later); err != nil {
			t.Fatal(err)
		}
		found, err := repo.FindUser(ctx, TestIssuer, owner.Subject)
		if err != nil || found.ID != owner.ID || *found.Email != email || *found.DisplayName != name || !found.UpdatedAt.Equal(later) || !found.CreatedAt.Equal(owner.CreatedAt) {
			t.Fatalf("found=%#v err=%v", found, err)
		}
		if err := repo.UpdateUserProfile(ctx, owner.ID, nil, nil, later); err != nil {
			t.Fatal(err)
		}
		if found, _ := repo.FindUser(ctx, TestIssuer, owner.Subject); found.Email != nil || found.DisplayName != nil {
			t.Fatalf("profile not cleared: %#v", found)
		}
		if err := repo.UpdateUserProfile(ctx, "usr_missing", nil, nil, later); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("missing user err = %v", err)
		}
	})

	t.Run("concurrent bootstrap succeeds exactly once", func(t *testing.T) {
		repo := newRepository(t)
		const callers = 10
		results := make(chan error, callers)
		var wait sync.WaitGroup
		for i := range callers {
			wait.Add(1)
			go func() {
				defer wait.Done()
				results <- repo.Bootstrap(ctx, testUser(i), domain.Workspace{ID: domain.WorkspaceID(fmt.Sprintf("wsp_%d", i)), Name: "Default workspace"})
			}()
		}
		wait.Wait()
		close(results)
		succeeded := 0
		for err := range results {
			switch {
			case err == nil:
				succeeded++
			case !errors.Is(err, repository.ErrAlreadyInitialized):
				t.Errorf("unexpected bootstrap error: %v", err)
			}
		}
		if succeeded != 1 {
			t.Fatalf("%d bootstraps succeeded, want exactly 1", succeeded)
		}
	})
}
