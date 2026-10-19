package application_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

func TestConcurrentConfigurationPatchesAreAtomic(t *testing.T) {
	repository := memory.NewConfigurationRepository(application.DefaultConfiguration())
	credentials := memory.NewCredentialRepository()
	service := application.NewConfigurationService(repository, credentials)
	workspaceID := domain.WorkspaceID("workspace-test")
	enabled := true

	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		if _, _, err := service.Update(context.Background(), workspaceID, application.ConfigurationPatch{AnalyticsEnabled: &enabled}); err != nil {
			t.Errorf("privacy patch: %v", err)
		}
	}()
	go func() {
		defer wait.Done()
		if _, _, err := service.Update(context.Background(), workspaceID, application.ConfigurationPatch{ProcessingComplete: &enabled}); err != nil {
			t.Errorf("notification patch: %v", err)
		}
	}()
	wait.Wait()

	configuration, _, err := service.Get(context.Background(), workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if !configuration.AnalyticsEnabled || !configuration.ProcessingComplete {
		t.Fatalf("concurrent patches lost an update: %#v", configuration)
	}
}

func TestMeetingServiceMapsRepositoryMissToNotFound(t *testing.T) {
	service := application.NewMeetingService(memory.NewMeetingRepository())
	_, err := service.Get(context.Background(), "workspace-test", "missing")
	var applicationError *application.Error
	if !errors.As(err, &applicationError) || applicationError.Code != application.NotFound {
		t.Fatalf("error = %v, want not_found application error", err)
	}
}

func TestMeetingServiceRejectsBlankDeleteID(t *testing.T) {
	service := application.NewMeetingService(memory.NewMeetingRepository())
	err := service.Delete(context.Background(), "workspace-test", " ")
	var applicationError *application.Error
	if !errors.As(err, &applicationError) || applicationError.Code != application.Validation {
		t.Fatalf("error = %v, want validation application error", err)
	}
}

func TestCredentialConfiguredStateIsWorkspaceScoped(t *testing.T) {
	repository := memory.NewCredentialRepository()
	service := application.NewCredentialService(repository)
	if _, err := service.Set(context.Background(), "workspace-a", "openai", domain.NewCredentialSecret([]byte("secret"))); err != nil {
		t.Fatal(err)
	}
	configured, err := service.IsConfigured(context.Background(), "workspace-a", "openai")
	if err != nil || !configured {
		t.Fatalf("workspace-a configured=%v err=%v", configured, err)
	}
	configured, err = service.IsConfigured(context.Background(), "workspace-b", "openai")
	if err != nil || configured {
		t.Fatalf("workspace-b configured=%v err=%v", configured, err)
	}
}

type unavailableMeetings struct{ *memory.MeetingRepository }

func (unavailableMeetings) List(context.Context, domain.WorkspaceID, *repository.MeetingPosition, int) ([]domain.Meeting, bool, error) {
	return nil, false, fmt.Errorf("postgres list meetings: %w", repository.ErrUnavailable)
}
func (unavailableMeetings) Get(context.Context, domain.WorkspaceID, domain.MeetingID) (domain.Meeting, error) {
	return domain.Meeting{}, repository.ErrNotFound
}

func TestStorageOutagesMapToUnavailableButClientErrorsDoNot(t *testing.T) {
	service := application.NewMeetingService(unavailableMeetings{memory.NewMeetingRepository()})
	var applicationError *application.Error

	_, err := service.List(context.Background(), "workspace-test", "", 10)
	if !errors.As(err, &applicationError) || applicationError.Code != application.Unavailable || applicationError.Op != "meetings.list" {
		t.Fatalf("outage err = %#v, want unavailable meetings.list", err)
	}
	_, err = service.List(context.Background(), "workspace-test", "garbage", 10)
	if !errors.As(err, &applicationError) || applicationError.Code != application.Validation {
		t.Fatalf("malformed cursor err = %v, want validation", err)
	}
	_, err = service.Get(context.Background(), "workspace-test", "missing")
	if !errors.As(err, &applicationError) || applicationError.Code != application.NotFound {
		t.Fatalf("missing err = %v, want not_found", err)
	}
}

func TestCredentialServiceRejectsBlankSecret(t *testing.T) {
	service := application.NewCredentialService(memory.NewCredentialRepository())
	_, err := service.Set(context.Background(), "workspace-a", "openai", domain.NewCredentialSecret([]byte("  ")))
	var applicationError *application.Error
	if !errors.As(err, &applicationError) || applicationError.Code != application.Validation {
		t.Fatalf("err = %v, want validation", err)
	}
}
