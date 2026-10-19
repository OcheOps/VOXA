package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/credentialcrypto"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// CredentialRepository stores provider secrets encrypted with AES-256-GCM.
// Plaintext is sealed in process before any SQL is issued; metadata reads
// never decrypt. Decryption happens only in RevealCredential.
type CredentialRepository struct {
	pool   *pgxpool.Pool
	cipher *credentialcrypto.Cipher
}

func NewCredentialRepository(pool *pgxpool.Pool, cipher *credentialcrypto.Cipher) *CredentialRepository {
	return &CredentialRepository{pool: pool, cipher: cipher}
}

func (r *CredentialRepository) List(ctx context.Context, workspaceID domain.WorkspaceID) ([]domain.CredentialStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := r.pool.Query(ctx, `SELECT provider FROM workspace_credentials WHERE workspace_id = $1 ORDER BY provider`, string(workspaceID))
	if err != nil {
		return nil, translate("list credentials", err)
	}
	statuses, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.CredentialStatus, error) {
		status := domain.CredentialStatus{Configured: true}
		return status, row.Scan(&status.Provider)
	})
	if err != nil {
		return nil, translate("list credentials", err)
	}
	return statuses, nil
}

func (r *CredentialRepository) IsConfigured(ctx context.Context, workspaceID domain.WorkspaceID, provider string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var configured bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workspace_credentials WHERE workspace_id = $1 AND provider = $2)`,
		string(workspaceID), provider).Scan(&configured)
	if err != nil {
		return false, translate("check credential", err)
	}
	return configured, nil
}

// Set seals the secret under the active key with a fresh nonce and upserts
// the result. created_at is kept on update.
func (r *CredentialRepository) Set(ctx context.Context, workspaceID domain.WorkspaceID, provider string, secret domain.CredentialSecret) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	sealed, err := r.cipher.Seal(ctx, workspaceID, provider, secret)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO workspace_credentials (workspace_id, provider, key_version, nonce, ciphertext)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, provider) DO UPDATE
SET key_version = EXCLUDED.key_version, nonce = EXCLUDED.nonce, ciphertext = EXCLUDED.ciphertext, updated_at = now()`,
		string(workspaceID), provider, sealed.KeyVersion, sealed.Nonce, sealed.Ciphertext)
	if err != nil {
		return translate("set credential", err)
	}
	return nil
}

// Delete is idempotent, matching the protocol's DELETE semantics.
func (r *CredentialRepository) Delete(ctx context.Context, workspaceID domain.WorkspaceID, provider string) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	if _, err := r.pool.Exec(ctx, `DELETE FROM workspace_credentials WHERE workspace_id = $1 AND provider = $2`, string(workspaceID), provider); err != nil {
		return translate("delete credential", err)
	}
	return nil
}

// RevealCredential implements repository.CredentialSecretReader. Decryption
// failures return *repository.CredentialUnreadableError with safe metadata.
func (r *CredentialRepository) RevealCredential(ctx context.Context, workspaceID domain.WorkspaceID, provider string) (domain.CredentialSecret, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var sealed credentialcrypto.Sealed
	err := r.pool.QueryRow(ctx, `SELECT key_version, nonce, ciphertext FROM workspace_credentials WHERE workspace_id = $1 AND provider = $2`,
		string(workspaceID), provider).Scan(&sealed.KeyVersion, &sealed.Nonce, &sealed.Ciphertext)
	if err != nil {
		return domain.CredentialSecret{}, translate("reveal credential", err)
	}
	secret, err := r.cipher.Open(ctx, workspaceID, provider, sealed)
	if err != nil {
		return domain.CredentialSecret{}, &repository.CredentialUnreadableError{WorkspaceID: workspaceID, Provider: provider, KeyVersion: sealed.KeyVersion, Reason: err}
	}
	return secret, nil
}

// CredentialKeyVersions returns the distinct key versions referenced by
// stored credentials, for startup checks and rotation tooling.
func CredentialKeyVersions(ctx context.Context, pool *pgxpool.Pool) (map[string]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := pool.Query(ctx, `SELECT key_version, count(*) FROM workspace_credentials GROUP BY key_version`)
	if err != nil {
		return nil, translate("list credential key versions", err)
	}
	defer rows.Close()
	versions := make(map[string]int64)
	for rows.Next() {
		var version string
		var count int64
		if err := rows.Scan(&version, &count); err != nil {
			return nil, translate("list credential key versions", err)
		}
		versions[version] = count
	}
	if err := rows.Err(); err != nil {
		return nil, translate("list credential key versions", err)
	}
	return versions, nil
}

var _ repository.CredentialRepository = (*CredentialRepository)(nil)
var _ repository.CredentialSecretReader = (*CredentialRepository)(nil)
