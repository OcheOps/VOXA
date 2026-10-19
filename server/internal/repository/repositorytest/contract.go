// Package repositorytest holds behavioral contract tests that every
// repository adapter (memory, PostgreSQL) must pass identically.
package repositorytest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// Workspaces used by the contracts. Adapters that enforce referential
// integrity must provision them in their factory.
const (
	WorkspaceA domain.WorkspaceID = "workspace-a"
	WorkspaceB domain.WorkspaceID = "workspace-b"
)

var baseTime = time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)

func meeting(workspaceID domain.WorkspaceID, id string, offset time.Duration) domain.Meeting {
	created := baseTime.Add(offset)
	return domain.Meeting{ID: domain.MeetingID(id), WorkspaceID: workspaceID, Title: "Meeting " + id, Status: domain.MeetingCreated, CreatedAt: created, UpdatedAt: created}
}

func assertMeeting(t *testing.T, got, want domain.Meeting) {
	t.Helper()
	if got.ID != want.ID || got.WorkspaceID != want.WorkspaceID || got.Title != want.Title || got.Status != want.Status ||
		!got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("meeting = %#v, want %#v", got, want)
	}
}

func ids(items []domain.Meeting) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, string(item.ID))
	}
	return result
}

func mustCreate(t *testing.T, repo repository.MeetingRepository, meetings ...domain.Meeting) {
	t.Helper()
	for _, m := range meetings {
		if err := repo.Create(context.Background(), m); err != nil {
			t.Fatalf("create %s: %v", m.ID, err)
		}
	}
}

// MeetingRepositoryContract runs the shared meeting semantics. newRepository
// must return an empty repository on every call.
func MeetingRepositoryContract(t *testing.T, newRepository func(t *testing.T) repository.MeetingRepository) {
	ctx := context.Background()

	t.Run("create and get round trip", func(t *testing.T) {
		repo := newRepository(t)
		want := meeting(WorkspaceA, "mtg_1", 0)
		mustCreate(t, repo, want)
		got, err := repo.Get(ctx, WorkspaceA, want.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertMeeting(t, got, want)
	})

	t.Run("duplicate create conflicts", func(t *testing.T) {
		repo := newRepository(t)
		mustCreate(t, repo, meeting(WorkspaceA, "mtg_1", 0))
		if err := repo.Create(ctx, meeting(WorkspaceA, "mtg_1", time.Second)); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("err = %v, want ErrConflict", err)
		}
	})

	t.Run("missing meeting is not found", func(t *testing.T) {
		repo := newRepository(t)
		if _, err := repo.Get(ctx, WorkspaceA, "missing"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("get err = %v", err)
		}
		if err := repo.Update(ctx, meeting(WorkspaceA, "missing", 0)); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("update err = %v", err)
		}
		if err := repo.Delete(ctx, WorkspaceA, "missing"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("delete err = %v", err)
		}
	})

	t.Run("update replaces mutable fields", func(t *testing.T) {
		repo := newRepository(t)
		original := meeting(WorkspaceA, "mtg_1", 0)
		mustCreate(t, repo, original)
		changed := original
		changed.Title = "Renamed"
		changed.UpdatedAt = original.UpdatedAt.Add(time.Minute)
		if err := repo.Update(ctx, changed); err != nil {
			t.Fatal(err)
		}
		got, err := repo.Get(ctx, WorkspaceA, original.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertMeeting(t, got, changed)
	})

	t.Run("delete removes meeting", func(t *testing.T) {
		repo := newRepository(t)
		mustCreate(t, repo, meeting(WorkspaceA, "mtg_1", 0))
		if err := repo.Delete(ctx, WorkspaceA, "mtg_1"); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Get(ctx, WorkspaceA, "mtg_1"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("get after delete err = %v", err)
		}
	})

	t.Run("workspace isolation", func(t *testing.T) {
		repo := newRepository(t)
		a := meeting(WorkspaceA, "mtg_a", 0)
		b := meeting(WorkspaceB, "mtg_b", 0)
		mustCreate(t, repo, a, b)
		if _, err := repo.Get(ctx, WorkspaceB, a.ID); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace get err = %v", err)
		}
		foreign := a
		foreign.WorkspaceID = WorkspaceB
		foreign.Title = "hijacked"
		if err := repo.Update(ctx, foreign); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace update err = %v", err)
		}
		if err := repo.Delete(ctx, WorkspaceB, a.ID); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace delete err = %v", err)
		}
		got, err := repo.Get(ctx, WorkspaceA, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertMeeting(t, got, a)
		items, _, err := repo.List(ctx, WorkspaceA, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(ids(items)) != "[mtg_a]" {
			t.Fatalf("workspace-a list = %v", ids(items))
		}
	})

	t.Run("list orders by created_at then id", func(t *testing.T) {
		repo := newRepository(t)
		// Inserted out of order; mtg_b/mtg_c tie on created_at.
		mustCreate(t, repo,
			meeting(WorkspaceA, "mtg_d", 3*time.Second),
			meeting(WorkspaceA, "mtg_c", time.Second),
			meeting(WorkspaceA, "mtg_a", 0),
			meeting(WorkspaceA, "mtg_b", time.Second),
			// Upper-case sorts before lower-case byte-wise; guards against
			// locale collation reordering ids.
			meeting(WorkspaceA, "mtg_B", time.Second),
		)
		items, hasMore, err := repo.List(ctx, WorkspaceA, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(ids(items)); got != "[mtg_a mtg_B mtg_b mtg_c mtg_d]" || hasMore {
			t.Fatalf("order = %s hasMore=%v", got, hasMore)
		}
	})

	t.Run("keyset pagination", func(t *testing.T) {
		repo := newRepository(t)
		for i := 0; i < 7; i++ {
			mustCreate(t, repo, meeting(WorkspaceA, fmt.Sprintf("mtg_%d", i), time.Duration(i/2)*time.Second))
		}
		var seen []string
		var after *repository.MeetingPosition
		var pages []int
		for {
			items, hasMore, err := repo.List(ctx, WorkspaceA, after, 3)
			if err != nil {
				t.Fatal(err)
			}
			pages = append(pages, len(items))
			seen = append(seen, ids(items)...)
			if !hasMore {
				break
			}
			last := items[len(items)-1]
			after = &repository.MeetingPosition{CreatedAt: last.CreatedAt, ID: last.ID}
		}
		if fmt.Sprint(pages) != "[3 3 1]" || strings.Join(seen, ",") != "mtg_0,mtg_1,mtg_2,mtg_3,mtg_4,mtg_5,mtg_6" {
			t.Fatalf("pages=%v seen=%v", pages, seen)
		}
	})

	t.Run("inserts between pages follow keyset semantics", func(t *testing.T) {
		repo := newRepository(t)
		mustCreate(t, repo, meeting(WorkspaceA, "mtg_1", time.Second), meeting(WorkspaceA, "mtg_2", 2*time.Second), meeting(WorkspaceA, "mtg_3", 3*time.Second))
		first, hasMore, err := repo.List(ctx, WorkspaceA, nil, 2)
		if err != nil || !hasMore {
			t.Fatalf("first page hasMore=%v err=%v", hasMore, err)
		}
		// One insert before the cursor (never shown), one after (shown).
		mustCreate(t, repo, meeting(WorkspaceA, "mtg_0", 0), meeting(WorkspaceA, "mtg_4", 4*time.Second))
		last := first[len(first)-1]
		second, hasMore, err := repo.List(ctx, WorkspaceA, &repository.MeetingPosition{CreatedAt: last.CreatedAt, ID: last.ID}, 2)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(ids(first), ids(second)); got != "[mtg_1 mtg_2] [mtg_3 mtg_4]" || hasMore {
			t.Fatalf("pages = %s hasMore=%v", got, hasMore)
		}
	})
}

// ConfigurationRepositoryContract runs the shared configuration semantics.
// newRepository must return an empty repository using defaults.
func ConfigurationRepositoryContract(t *testing.T, defaults domain.Configuration, newRepository func(t *testing.T) repository.ConfigurationRepository) {
	ctx := context.Background()

	t.Run("missing configuration returns defaults", func(t *testing.T) {
		repo := newRepository(t)
		got, err := repo.Get(ctx, WorkspaceA)
		if err != nil {
			t.Fatal(err)
		}
		assertConfiguration(t, got, defaults)
	})

	t.Run("update persists and keeps untouched fields", func(t *testing.T) {
		repo := newRepository(t)
		template := "standard"
		if _, err := repo.Update(ctx, WorkspaceA, func(c domain.Configuration) (domain.Configuration, error) {
			c.Transcription.Provider = "whisper"
			c.Summary.TemplateID = &template
			return c, nil
		}); err != nil {
			t.Fatal(err)
		}
		returned, err := repo.Update(ctx, WorkspaceA, func(c domain.Configuration) (domain.Configuration, error) {
			c.AnalyticsEnabled = true
			return c, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		want := defaults
		want.Transcription.Provider = "whisper"
		want.Summary.TemplateID = &template
		want.AnalyticsEnabled = true
		assertConfiguration(t, returned, want)
		got, err := repo.Get(ctx, WorkspaceA)
		if err != nil {
			t.Fatal(err)
		}
		assertConfiguration(t, got, want)

		if _, err := repo.Update(ctx, WorkspaceA, func(c domain.Configuration) (domain.Configuration, error) {
			c.Summary.TemplateID = nil
			return c, nil
		}); err != nil {
			t.Fatal(err)
		}
		got, err = repo.Get(ctx, WorkspaceA)
		if err != nil {
			t.Fatal(err)
		}
		if got.Summary.TemplateID != nil {
			t.Fatalf("templateId = %v, want cleared", *got.Summary.TemplateID)
		}
	})

	t.Run("failed update is not persisted", func(t *testing.T) {
		repo := newRepository(t)
		sentinel := errors.New("rejected")
		_, err := repo.Update(ctx, WorkspaceA, func(c domain.Configuration) (domain.Configuration, error) {
			c.RetainMedia = !c.RetainMedia
			return c, sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want update error passed through", err)
		}
		got, err := repo.Get(ctx, WorkspaceA)
		if err != nil {
			t.Fatal(err)
		}
		assertConfiguration(t, got, defaults)
	})

	t.Run("workspace isolation", func(t *testing.T) {
		repo := newRepository(t)
		if _, err := repo.Update(ctx, WorkspaceA, func(c domain.Configuration) (domain.Configuration, error) {
			c.Summary.Model = "workspace-a-model"
			return c, nil
		}); err != nil {
			t.Fatal(err)
		}
		got, err := repo.Get(ctx, WorkspaceB)
		if err != nil {
			t.Fatal(err)
		}
		assertConfiguration(t, got, defaults)
	})

	t.Run("concurrent updates are atomic", func(t *testing.T) {
		repo := newRepository(t)
		const writers = 20
		var wait sync.WaitGroup
		wait.Add(writers)
		for i := 0; i < writers; i++ {
			go func() {
				defer wait.Done()
				// Read-modify-write that loses data unless updates serialize.
				if _, err := repo.Update(ctx, WorkspaceA, func(c domain.Configuration) (domain.Configuration, error) {
					c.Transcription.Model += "x"
					return c, nil
				}); err != nil {
					t.Errorf("update: %v", err)
				}
			}()
		}
		wait.Wait()
		got, err := repo.Get(ctx, WorkspaceA)
		if err != nil {
			t.Fatal(err)
		}
		if want := defaults.Transcription.Model + strings.Repeat("x", writers); got.Transcription.Model != want {
			t.Fatalf("model = %q, want %q (lost update)", got.Transcription.Model, want)
		}
	})
}

func assertConfiguration(t *testing.T, got, want domain.Configuration) {
	t.Helper()
	gotTemplate, wantTemplate := got.Summary.TemplateID, want.Summary.TemplateID
	got.Summary.TemplateID, want.Summary.TemplateID = nil, nil
	if got != want || (gotTemplate == nil) != (wantTemplate == nil) || gotTemplate != nil && *gotTemplate != *wantTemplate {
		t.Fatalf("configuration = %#v (template %v), want %#v (template %v)", got, gotTemplate, want, wantTemplate)
	}
}

// CredentialStore is the full credential surface an adapter provides:
// metadata/write operations plus the internal secret reader.
type CredentialStore interface {
	repository.CredentialRepository
	repository.CredentialSecretReader
}

// FakeSecret is an obviously identifiable value tests assert never leaks.
const FakeSecret = "sk-VOXA-FAKE-SECRET-0xDEADBEEF"

func newSecret(value string) domain.CredentialSecret {
	return domain.NewCredentialSecret([]byte(value))
}

func assertRevealed(t *testing.T, repo CredentialStore, workspaceID domain.WorkspaceID, provider, want string) {
	t.Helper()
	got, err := repo.RevealCredential(context.Background(), workspaceID, provider)
	if err != nil {
		t.Fatalf("reveal %s/%s: %v", workspaceID, provider, err)
	}
	if string(got.Expose()) != want {
		t.Fatalf("reveal %s/%s returned a different secret", workspaceID, provider)
	}
}

// CredentialRepositoryContract runs the shared credential semantics.
// newRepository must return an empty store on every call.
func CredentialRepositoryContract(t *testing.T, newRepository func(t *testing.T) CredentialStore) {
	ctx := context.Background()

	t.Run("set list check and reveal", func(t *testing.T) {
		repo := newRepository(t)
		if err := repo.Set(ctx, WorkspaceA, "openai", newSecret(FakeSecret)); err != nil {
			t.Fatal(err)
		}
		statuses, err := repo.List(ctx, WorkspaceA)
		if err != nil {
			t.Fatal(err)
		}
		if len(statuses) != 1 || statuses[0] != (domain.CredentialStatus{Provider: "openai", Configured: true}) {
			t.Fatalf("statuses = %#v", statuses)
		}
		configured, err := repo.IsConfigured(ctx, WorkspaceA, "openai")
		if err != nil || !configured {
			t.Fatalf("configured=%v err=%v", configured, err)
		}
		configured, err = repo.IsConfigured(ctx, WorkspaceA, "anthropic")
		if err != nil || configured {
			t.Fatalf("unset provider configured=%v err=%v", configured, err)
		}
		assertRevealed(t, repo, WorkspaceA, "openai", FakeSecret)
	})

	t.Run("caller wiping its secret does not affect storage", func(t *testing.T) {
		repo := newRepository(t)
		secret := newSecret(FakeSecret)
		if err := repo.Set(ctx, WorkspaceA, "openai", secret); err != nil {
			t.Fatal(err)
		}
		secret.Wipe()
		assertRevealed(t, repo, WorkspaceA, "openai", FakeSecret)
	})

	t.Run("update replaces secret", func(t *testing.T) {
		repo := newRepository(t)
		for _, value := range []string{"first-" + FakeSecret, "second-" + FakeSecret} {
			if err := repo.Set(ctx, WorkspaceA, "openai", newSecret(value)); err != nil {
				t.Fatal(err)
			}
		}
		assertRevealed(t, repo, WorkspaceA, "openai", "second-"+FakeSecret)
		if statuses, err := repo.List(ctx, WorkspaceA); err != nil || len(statuses) != 1 {
			t.Fatalf("statuses=%v err=%v", statuses, err)
		}
	})

	t.Run("delete removes and is idempotent", func(t *testing.T) {
		repo := newRepository(t)
		if err := repo.Set(ctx, WorkspaceA, "openai", newSecret(FakeSecret)); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := repo.Delete(ctx, WorkspaceA, "openai"); err != nil {
				t.Fatal(err)
			}
		}
		if statuses, err := repo.List(ctx, WorkspaceA); err != nil || len(statuses) != 0 {
			t.Fatalf("statuses=%v err=%v", statuses, err)
		}
		if _, err := repo.RevealCredential(ctx, WorkspaceA, "openai"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("reveal after delete err = %v", err)
		}
	})

	t.Run("list is ordered by provider", func(t *testing.T) {
		repo := newRepository(t)
		for _, provider := range []string{"openai", "Groq", "anthropic"} {
			if err := repo.Set(ctx, WorkspaceA, provider, newSecret(FakeSecret)); err != nil {
				t.Fatal(err)
			}
		}
		statuses, err := repo.List(ctx, WorkspaceA)
		if err != nil {
			t.Fatal(err)
		}
		var providers []string
		for _, status := range statuses {
			providers = append(providers, status.Provider)
		}
		if got := strings.Join(providers, ","); got != "Groq,anthropic,openai" {
			t.Fatalf("order = %s", got)
		}
	})

	t.Run("workspace isolation", func(t *testing.T) {
		repo := newRepository(t)
		if err := repo.Set(ctx, WorkspaceA, "openai", newSecret("a-"+FakeSecret)); err != nil {
			t.Fatal(err)
		}
		if statuses, err := repo.List(ctx, WorkspaceB); err != nil || len(statuses) != 0 {
			t.Fatalf("workspace-b statuses=%v err=%v", statuses, err)
		}
		if configured, err := repo.IsConfigured(ctx, WorkspaceB, "openai"); err != nil || configured {
			t.Fatalf("workspace-b configured=%v err=%v", configured, err)
		}
		if _, err := repo.RevealCredential(ctx, WorkspaceB, "openai"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace reveal err = %v", err)
		}
		if err := repo.Set(ctx, WorkspaceB, "openai", newSecret("b-"+FakeSecret)); err != nil {
			t.Fatal(err)
		}
		if err := repo.Delete(ctx, WorkspaceB, "openai"); err != nil {
			t.Fatal(err)
		}
		assertRevealed(t, repo, WorkspaceA, "openai", "a-"+FakeSecret)
	})
}
