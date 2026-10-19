package memory_test

import (
	"testing"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/repository"
	"github.com/OcheOps/voxa/server/internal/repository/repositorytest"
)

func TestMeetingRepositoryContract(t *testing.T) {
	repositorytest.MeetingRepositoryContract(t, func(*testing.T) repository.MeetingRepository {
		return memory.NewMeetingRepository()
	})
}

func TestConfigurationRepositoryContract(t *testing.T) {
	defaults := application.DefaultConfiguration()
	repositorytest.ConfigurationRepositoryContract(t, defaults, func(*testing.T) repository.ConfigurationRepository {
		return memory.NewConfigurationRepository(defaults)
	})
}

func TestCredentialRepositoryContract(t *testing.T) {
	repositorytest.CredentialRepositoryContract(t, func(*testing.T) repositorytest.CredentialStore {
		return memory.NewCredentialRepository()
	})
}
