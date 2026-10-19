package postgres_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/adapters/postgres"
	"github.com/OcheOps/voxa/server/internal/credentialcrypto"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
	"github.com/OcheOps/voxa/server/internal/repository/repositorytest"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, credentialcrypto.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func credentialCipher(t *testing.T, active string, keys map[string][]byte) *credentialcrypto.Cipher {
	t.Helper()
	ring, err := credentialcrypto.NewStaticKeyRing(active, keys)
	if err != nil {
		t.Fatal(err)
	}
	return credentialcrypto.NewCipher(ring)
}

type storedCredential struct {
	keyVersion string
	nonce      []byte
	ciphertext []byte
	createdAt  time.Time
	updatedAt  time.Time
	asText     string
}

func readStoredCredential(t *testing.T, pool *pgxpool.Pool, workspaceID domain.WorkspaceID, provider string) storedCredential {
	t.Helper()
	var row storedCredential
	err := pool.QueryRow(context.Background(), `SELECT key_version, nonce, ciphertext, created_at, updated_at, c::text
FROM workspace_credentials c WHERE workspace_id = $1 AND provider = $2`, string(workspaceID), provider).
		Scan(&row.keyVersion, &row.nonce, &row.ciphertext, &row.createdAt, &row.updatedAt, &row.asText)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestCredentialRepositoryContract(t *testing.T) {
	repositorytest.CredentialRepositoryContract(t, func(t *testing.T) repositorytest.CredentialStore {
		return postgres.NewCredentialRepository(migratedPool(t), credentialCipher(t, "v1", map[string][]byte{"v1": testKey(t)}))
	})
}

func TestCredentialRowsHoldOnlyCiphertext(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	key := testKey(t)
	repo := postgres.NewCredentialRepository(pool, credentialCipher(t, "v1", map[string][]byte{"v1": key}))
	if err := repo.Set(ctx, repositorytest.WorkspaceA, "openai", domain.NewCredentialSecret([]byte(repositorytest.FakeSecret))); err != nil {
		t.Fatal(err)
	}
	first := readStoredCredential(t, pool, repositorytest.WorkspaceA, "openai")
	for _, needle := range []string{repositorytest.FakeSecret, hex.EncodeToString([]byte(repositorytest.FakeSecret)), hex.EncodeToString(key)} {
		if strings.Contains(first.asText, needle) || bytes.Contains(first.ciphertext, []byte(needle)) {
			t.Fatal("database row contains plaintext secret or key material")
		}
	}
	if first.keyVersion != "v1" || len(first.nonce) != credentialcrypto.NonceSize || len(first.ciphertext) <= 16 {
		t.Fatalf("stored metadata version=%q nonce=%d ciphertext=%d", first.keyVersion, len(first.nonce), len(first.ciphertext))
	}

	// Same secret again: fresh nonce, different ciphertext, created_at kept.
	if err := repo.Set(ctx, repositorytest.WorkspaceA, "openai", domain.NewCredentialSecret([]byte(repositorytest.FakeSecret))); err != nil {
		t.Fatal(err)
	}
	second := readStoredCredential(t, pool, repositorytest.WorkspaceA, "openai")
	if bytes.Equal(first.nonce, second.nonce) || bytes.Equal(first.ciphertext, second.ciphertext) {
		t.Fatal("update reused nonce or ciphertext")
	}
	if !second.createdAt.Equal(first.createdAt) || second.updatedAt.Before(first.updatedAt) {
		t.Fatalf("timestamps created %v->%v updated %v->%v", first.createdAt, second.createdAt, first.updatedAt, second.updatedAt)
	}

	// A fresh repository and cipher with the same key (a restart) decrypts
	// using the stored key version.
	restarted := postgres.NewCredentialRepository(pool, credentialCipher(t, "v1", map[string][]byte{"v1": key}))
	secret, err := restarted.RevealCredential(ctx, repositorytest.WorkspaceA, "openai")
	if err != nil || string(secret.Expose()) != repositorytest.FakeSecret {
		t.Fatalf("reveal after restart err=%v", err)
	}
}

func TestCredentialRotationUsesStoredKeyVersion(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	oldKey, newKey := testKey(t), testKey(t)
	before := postgres.NewCredentialRepository(pool, credentialCipher(t, "v1", map[string][]byte{"v1": oldKey}))
	if err := before.Set(ctx, repositorytest.WorkspaceA, "openai", domain.NewCredentialSecret([]byte("old-"+repositorytest.FakeSecret))); err != nil {
		t.Fatal(err)
	}
	after := postgres.NewCredentialRepository(pool, credentialCipher(t, "v2", map[string][]byte{"v1": oldKey, "v2": newKey}))
	secret, err := after.RevealCredential(ctx, repositorytest.WorkspaceA, "openai")
	if err != nil || string(secret.Expose()) != "old-"+repositorytest.FakeSecret {
		t.Fatalf("old row unreadable after rotation: %v", err)
	}
	if err := after.Set(ctx, repositorytest.WorkspaceA, "anthropic", domain.NewCredentialSecret([]byte("new-"+repositorytest.FakeSecret))); err != nil {
		t.Fatal(err)
	}
	if got := readStoredCredential(t, pool, repositorytest.WorkspaceA, "anthropic").keyVersion; got != "v2" {
		t.Fatalf("new write key version = %q, want v2", got)
	}
	versions, err := postgres.CredentialKeyVersions(ctx, pool)
	if err != nil || versions["v1"] != 1 || versions["v2"] != 1 {
		t.Fatalf("key versions = %v err=%v", versions, err)
	}
}

func assertUnreadable(t *testing.T, err error, keyVersion string, reason error) {
	t.Helper()
	var unreadable *repository.CredentialUnreadableError
	if !errors.As(err, &unreadable) || !errors.Is(err, reason) || unreadable.KeyVersion != keyVersion {
		t.Fatalf("err = %v, want CredentialUnreadableError(%s, %v)", err, keyVersion, reason)
	}
	formatted := fmt.Sprintf("%v %+v %#v", err, err, unreadable.LogValue())
	if strings.Contains(formatted, repositorytest.FakeSecret) {
		t.Fatal("decryption error leaked plaintext")
	}
}

func TestCredentialDecryptionFailuresAreSafe(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	key := testKey(t)
	repo := postgres.NewCredentialRepository(pool, credentialCipher(t, "v1", map[string][]byte{"v1": key}))
	if err := repo.Set(ctx, repositorytest.WorkspaceA, "openai", domain.NewCredentialSecret([]byte(repositorytest.FakeSecret))); err != nil {
		t.Fatal(err)
	}

	t.Run("unknown key version", func(t *testing.T) {
		withoutV1 := postgres.NewCredentialRepository(pool, credentialCipher(t, "v2", map[string][]byte{"v2": testKey(t)}))
		_, err := withoutV1.RevealCredential(ctx, repositorytest.WorkspaceA, "openai")
		assertUnreadable(t, err, "v1", credentialcrypto.ErrUnknownKeyVersion)
		// Metadata reads never decrypt, so they keep working.
		if configured, err := withoutV1.IsConfigured(ctx, repositorytest.WorkspaceA, "openai"); err != nil || !configured {
			t.Fatalf("configured=%v err=%v", configured, err)
		}
	})

	t.Run("ciphertext copied to another workspace or provider", func(t *testing.T) {
		for _, target := range []struct {
			workspace domain.WorkspaceID
			provider  string
		}{{repositorytest.WorkspaceB, "openai"}, {repositorytest.WorkspaceA, "anthropic"}} {
			if _, err := pool.Exec(ctx, `INSERT INTO workspace_credentials (workspace_id, provider, key_version, nonce, ciphertext)
SELECT $1, $2, key_version, nonce, ciphertext FROM workspace_credentials WHERE workspace_id = $3 AND provider = 'openai'`,
				string(target.workspace), target.provider, string(repositorytest.WorkspaceA)); err != nil {
				t.Fatal(err)
			}
			_, err := repo.RevealCredential(ctx, target.workspace, target.provider)
			assertUnreadable(t, err, "v1", credentialcrypto.ErrDecrypt)
		}
	})

	t.Run("corrupted ciphertext", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE workspace_credentials SET ciphertext = set_byte(ciphertext, 0, get_byte(ciphertext, 0) # 1)
WHERE workspace_id = $1 AND provider = 'openai'`, string(repositorytest.WorkspaceA)); err != nil {
			t.Fatal(err)
		}
		_, err := repo.RevealCredential(ctx, repositorytest.WorkspaceA, "openai")
		assertUnreadable(t, err, "v1", credentialcrypto.ErrDecrypt)
	})
}

func TestCredentialSchemaRejectsMalformedRows(t *testing.T) {
	pool := migratedPool(t)
	ctx := context.Background()
	cases := map[string]string{
		"short nonce":      `INSERT INTO workspace_credentials (workspace_id, provider, key_version, nonce, ciphertext) VALUES ($1, 'openai', 'v1', '\x00'::bytea, decode(repeat('00', 32), 'hex'))`,
		"tag-only payload": `INSERT INTO workspace_credentials (workspace_id, provider, key_version, nonce, ciphertext) VALUES ($1, 'openai', 'v1', decode(repeat('00', 12), 'hex'), decode(repeat('00', 16), 'hex'))`,
		"bad provider":     `INSERT INTO workspace_credentials (workspace_id, provider, key_version, nonce, ciphertext) VALUES ($1, 'bad provider', 'v1', decode(repeat('00', 12), 'hex'), decode(repeat('00', 32), 'hex'))`,
		"bad key version":  `INSERT INTO workspace_credentials (workspace_id, provider, key_version, nonce, ciphertext) VALUES ($1, 'openai', '', decode(repeat('00', 12), 'hex'), decode(repeat('00', 32), 'hex'))`,
	}
	for name, statement := range cases {
		if _, err := pool.Exec(ctx, statement, string(repositorytest.WorkspaceA)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
