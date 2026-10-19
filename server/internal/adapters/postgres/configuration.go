package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/domain"
)

const configurationColumns = `transcription_provider, transcription_model, transcription_language, transcription_translate_to_english,
summary_provider, summary_model, summary_output_language, summary_template_id, summary_auto_generate,
recording_retain_media, privacy_analytics_enabled, notifications_processing_complete`

// ConfigurationRepository stores one typed row per workspace. A missing row
// reads as the application-supplied defaults.
type ConfigurationRepository struct {
	pool     *pgxpool.Pool
	defaults domain.Configuration
}

func NewConfigurationRepository(pool *pgxpool.Pool, defaults domain.Configuration) *ConfigurationRepository {
	return &ConfigurationRepository{pool: pool, defaults: defaults}
}

func (r *ConfigurationRepository) Get(ctx context.Context, workspaceID domain.WorkspaceID) (domain.Configuration, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	config, err := scanConfiguration(r.pool.QueryRow(ctx, `SELECT `+configurationColumns+` FROM workspace_configuration WHERE workspace_id = $1`, string(workspaceID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return r.defaults, nil
	}
	if err != nil {
		return domain.Configuration{}, translate("get configuration", err)
	}
	return config, nil
}

// Update performs an atomic read-modify-write: the row is materialized from
// defaults if absent, locked with FOR UPDATE, passed to update, and written in
// the same transaction. Concurrent patches serialize on the row lock, so none
// is lost. Errors returned by update are passed through unchanged and roll
// the transaction back.
func (r *ConfigurationRepository) Update(ctx context.Context, workspaceID domain.WorkspaceID, update func(domain.Configuration) (domain.Configuration, error)) (domain.Configuration, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Configuration{}, translate("begin configuration update", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `INSERT INTO workspace_configuration (workspace_id, `+configurationColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (workspace_id) DO NOTHING`, configurationArgs(workspaceID, r.defaults)...); err != nil {
		return domain.Configuration{}, translate("initialize configuration", err)
	}
	current, err := scanConfiguration(tx.QueryRow(ctx, `SELECT `+configurationColumns+` FROM workspace_configuration WHERE workspace_id = $1 FOR UPDATE`, string(workspaceID)))
	if err != nil {
		return domain.Configuration{}, translate("lock configuration", err)
	}
	next, err := update(current)
	if err != nil {
		return domain.Configuration{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE workspace_configuration SET
transcription_provider = $2, transcription_model = $3, transcription_language = $4, transcription_translate_to_english = $5,
summary_provider = $6, summary_model = $7, summary_output_language = $8, summary_template_id = $9, summary_auto_generate = $10,
recording_retain_media = $11, privacy_analytics_enabled = $12, notifications_processing_complete = $13,
updated_at = now()
WHERE workspace_id = $1`, configurationArgs(workspaceID, next)...); err != nil {
		return domain.Configuration{}, translate("update configuration", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Configuration{}, translate("commit configuration", err)
	}
	return next, nil
}

func configurationArgs(workspaceID domain.WorkspaceID, c domain.Configuration) []any {
	return []any{
		string(workspaceID),
		c.Transcription.Provider, c.Transcription.Model, c.Transcription.Language, c.Transcription.TranslateToEnglish,
		c.Summary.Provider, c.Summary.Model, c.Summary.OutputLanguage, c.Summary.TemplateID, c.Summary.AutoGenerate,
		c.RetainMedia, c.AnalyticsEnabled, c.ProcessingComplete,
	}
}

func scanConfiguration(row pgx.Row) (domain.Configuration, error) {
	var c domain.Configuration
	err := row.Scan(
		&c.Transcription.Provider, &c.Transcription.Model, &c.Transcription.Language, &c.Transcription.TranslateToEnglish,
		&c.Summary.Provider, &c.Summary.Model, &c.Summary.OutputLanguage, &c.Summary.TemplateID, &c.Summary.AutoGenerate,
		&c.RetainMedia, &c.AnalyticsEnabled, &c.ProcessingComplete,
	)
	return c, err
}
