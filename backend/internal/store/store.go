package store

import (
	"context"
	"crypto/x509"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hkjang/moina/backend/internal/model"
	"github.com/hkjang/moina/backend/internal/secure"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var (
	ErrNotFound = pgx.ErrNoRows
	ErrRevoked  = errors.New("폐기된 키입니다")
)

type Store struct{ pool *pgxpool.Pool }

const (
	privateCAPath = "/etc/moina/certs/ca-certificates.crt"
	systemCAPath  = "/etc/ssl/certs/ca-certificates.crt"
)

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("PostgreSQL DSN 해석 실패: %w", err)
	}
	if err := addPrivateCAToPostgres(cfg); err != nil {
		return nil, err
	}
	cfg.MaxConns = 25
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s := &Store{pool: pool}
	if err := s.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func addPrivateCAToPostgres(cfg *pgxpool.Config) error {
	if cfg.ConnConfig.TLSConfig == nil {
		return nil
	}
	privatePEM, err := os.ReadFile(privateCAPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("PostgreSQL private CA 읽기 실패: %w", err)
	}
	tlsConfig := cfg.ConnConfig.TLSConfig.Clone()
	pool := tlsConfig.RootCAs
	if pool == nil {
		pool = x509.NewCertPool()
		if systemPEM, systemErr := os.ReadFile(systemCAPath); systemErr == nil {
			if !pool.AppendCertsFromPEM(systemPEM) {
				return errors.New("PostgreSQL 시스템 CA 파일이 올바르지 않습니다")
			}
		} else if !errors.Is(systemErr, os.ErrNotExist) {
			return fmt.Errorf("PostgreSQL 시스템 CA 읽기 실패: %w", systemErr)
		}
	}
	if !pool.AppendCertsFromPEM(privatePEM) {
		return errors.New("PostgreSQL private CA 파일에 유효한 인증서가 없습니다")
	}
	tlsConfig.RootCAs = pool
	cfg.ConnConfig.TLSConfig = tlsConfig
	for _, fallback := range cfg.ConnConfig.Fallbacks {
		if fallback.TLSConfig != nil {
			fallbackTLS := fallback.TLSConfig.Clone()
			fallbackTLS.RootCAs = pool
			fallback.TLSConfig = fallbackTLS
		}
	}
	return nil
}

func (s *Store) Pool() *pgxpool.Pool            { return s.pool }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }
func (s *Store) Close()                         { s.pool.Close() }

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1297042026)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var applied bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, entry.Name()).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); err != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, entry.Name()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

const userColumns = `id,username,display_name,email,bio,avatar_id,account_type,provider,roles,active,created_at,updated_at`

func scanUser(row pgx.Row) (model.User, error) {
	var user model.User
	err := row.Scan(&user.ID, &user.Username, &user.DisplayName, &user.Email, &user.Bio, &user.AvatarID, &user.AccountType, &user.Provider, &user.Roles, &user.Active, &user.CreatedAt, &user.UpdatedAt)
	return user, err
}

func (s *Store) BootstrapAdmin(ctx context.Context, username, passwordHash string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, provider string
	var roles []string
	var active bool
	err = tx.QueryRow(ctx, `SELECT id,provider,roles,active FROM users WHERE lower(username)=lower($1) FOR UPDATE`, username).Scan(&id, &provider, &roles, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO users(id,username,display_name,password_hash,account_type,provider,roles,active) VALUES($1,$2,$2,$3,'human','local',ARRAY['super_admin','admin']::text[],true)`, secure.NewID("usr"), username, passwordHash)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if provider != "local" || !active || !contains(roles, model.RoleSuperAdmin) {
		return errors.New("bootstrap 관리자 아이디가 관리자 아닌 기존 계정과 충돌합니다")
	}
	return tx.Commit(ctx)
}

func (s *Store) UserByUsername(ctx context.Context, username string) (model.UserAuth, error) {
	var auth model.UserAuth
	err := s.pool.QueryRow(ctx, `SELECT `+userColumns+`,password_hash FROM users WHERE lower(username)=lower($1)`, username).Scan(
		&auth.ID, &auth.Username, &auth.DisplayName, &auth.Email, &auth.Bio, &auth.AvatarID, &auth.AccountType, &auth.Provider, &auth.Roles, &auth.Active, &auth.CreatedAt, &auth.UpdatedAt, &auth.PasswordHash,
	)
	return auth, err
}

func (s *Store) UserByID(ctx context.Context, id string) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1`, id))
}

func (s *Store) UserByOIDC(ctx context.Context, issuer, subject string) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT u.`+strings.ReplaceAll(userColumns, ",", ",u.")+` FROM users u JOIN oidc_identities i ON i.user_id=u.id WHERE i.issuer=$1 AND i.subject=$2`, issuer, subject))
}

func (s *Store) UpsertOIDCUser(ctx context.Context, candidate model.User, issuer, subject string) (model.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.User{}, err
	}
	defer tx.Rollback(ctx)
	var userID string
	err = tx.QueryRow(ctx, `SELECT user_id FROM oidc_identities WHERE issuer=$1 AND subject=$2 FOR UPDATE`, issuer, subject).Scan(&userID)
	if err == nil {
		user, updateErr := scanUser(tx.QueryRow(ctx, `UPDATE users SET display_name=$2,email=$3,updated_at=now() WHERE id=$1 RETURNING `+userColumns, userID, candidate.DisplayName, candidate.Email))
		if updateErr != nil {
			return model.User{}, updateErr
		}
		if _, updateErr = tx.Exec(ctx, `UPDATE oidc_identities SET last_login_at=now() WHERE issuer=$1 AND subject=$2`, issuer, subject); updateErr != nil {
			return model.User{}, updateErr
		}
		return user, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.User{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO users(id,username,display_name,email,account_type,provider,roles,active) VALUES($1,$2,$3,$4,'human','oidc',$5,true)`, candidate.ID, candidate.Username, candidate.DisplayName, candidate.Email, candidate.Roles)
	if err != nil {
		return model.User{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO oidc_identities(issuer,subject,user_id) VALUES($1,$2,$3)`, issuer, subject, candidate.ID); err != nil {
		return model.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.User{}, err
	}
	return s.UserByID(ctx, candidate.ID)
}

func (s *Store) CreateSession(ctx context.Context, session model.Session) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO sessions(id,user_id,token_hash,csrf_hash,expires_at) VALUES($1,$2,$3,$4,$5)`, session.ID, session.UserID, session.TokenHash, session.CSRFHash, session.ExpiresAt)
	return err
}

func (s *Store) SessionUser(ctx context.Context, tokenHash string) (model.User, string, error) {
	var user model.User
	var csrfHash string
	err := s.pool.QueryRow(ctx, `SELECT u.`+strings.ReplaceAll(userColumns, ",", ",u.")+`,s.csrf_hash FROM users u JOIN sessions s ON s.user_id=u.id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.active`, tokenHash).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.Email, &user.Bio, &user.AvatarID, &user.AccountType, &user.Provider, &user.Roles, &user.Active, &user.CreatedAt, &user.UpdatedAt, &csrfHash,
	)
	return user, csrfHash, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, tokenHash)
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at<=now()`)
	return err
}

func (s *Store) UpdatePassword(ctx context.Context, userID, passwordHash string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET password_hash=$2,updated_at=now() WHERE id=$1 AND provider='local'`, userID, passwordHash)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}

func (s *Store) GetSetting(ctx context.Context, key string) (model.SettingRecord, error) {
	var setting model.SettingRecord
	err := s.pool.QueryRow(ctx, `SELECT key,payload,sensitive,revision,updated_by,updated_at FROM settings WHERE key=$1`, key).Scan(&setting.Key, &setting.Payload, &setting.Sensitive, &setting.Revision, &setting.UpdatedBy, &setting.UpdatedAt)
	return setting, err
}

func (s *Store) ListSettings(ctx context.Context) ([]model.SettingRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT key,payload,sensitive,revision,updated_by,updated_at FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.SettingRecord, 0)
	for rows.Next() {
		var item model.SettingRecord
		if err := rows.Scan(&item.Key, &item.Payload, &item.Sensitive, &item.Revision, &item.UpdatedBy, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) PutSetting(ctx context.Context, setting model.SettingRecord, expectedRevision *int64) (model.SettingRecord, error) {
	if expectedRevision == nil {
		return scanSetting(s.pool.QueryRow(ctx, `INSERT INTO settings(key,payload,sensitive,updated_by) VALUES($1,$2,$3,$4) ON CONFLICT(key) DO UPDATE SET payload=EXCLUDED.payload,sensitive=EXCLUDED.sensitive,updated_by=EXCLUDED.updated_by,revision=settings.revision+1,updated_at=now() RETURNING key,payload,sensitive,revision,updated_by,updated_at`, setting.Key, setting.Payload, setting.Sensitive, setting.UpdatedBy))
	}
	return scanSetting(s.pool.QueryRow(ctx, `UPDATE settings SET payload=$2,sensitive=$3,updated_by=$4,revision=revision+1,updated_at=now() WHERE key=$1 AND revision=$5 RETURNING key,payload,sensitive,revision,updated_by,updated_at`, setting.Key, setting.Payload, setting.Sensitive, setting.UpdatedBy, *expectedRevision))
}

func scanSetting(row pgx.Row) (model.SettingRecord, error) {
	var setting model.SettingRecord
	err := row.Scan(&setting.Key, &setting.Payload, &setting.Sensitive, &setting.Revision, &setting.UpdatedBy, &setting.UpdatedAt)
	return setting, err
}

func (s *Store) GetPreference(ctx context.Context, userID string) (json.RawMessage, error) {
	var value json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT payload FROM user_preferences WHERE user_id=$1`, userID).Scan(&value)
	return value, err
}

func (s *Store) PutPreference(ctx context.Context, userID string, value json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO user_preferences(user_id,payload) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET payload=EXCLUDED.payload,updated_at=now()`, userID, value)
	return err
}

func (s *Store) PermissionsForRoles(ctx context.Context, roles []string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT permission FROM role_permissions WHERE role_name=ANY($1) ORDER BY permission`, roles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	permissions := make([]string, 0)
	for rows.Next() {
		var permission string
		if err := rows.Scan(&permission); err != nil {
			return nil, err
		}
		permissions = append(permissions, permission)
	}
	return permissions, rows.Err()
}

func (s *Store) APIKeyUser(ctx context.Context, tokenHash string) (model.User, model.APIKey, error) {
	var user model.User
	var key model.APIKey
	err := s.pool.QueryRow(ctx, `SELECT u.`+strings.ReplaceAll(userColumns, ",", ",u.")+`,k.id,k.user_id,k.name,k.prefix,k.permissions,k.version,k.created_at,k.rotated_at,k.expires_at,k.revoked_at,k.last_used_at,k.token_hash FROM users u JOIN api_keys k ON k.user_id=u.id WHERE k.token_hash=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()) AND u.active`, tokenHash).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.Email, &user.Bio, &user.AvatarID, &user.AccountType, &user.Provider, &user.Roles, &user.Active, &user.CreatedAt, &user.UpdatedAt,
		&key.ID, &key.UserID, &key.Name, &key.Prefix, &key.Permissions, &key.Version, &key.CreatedAt, &key.RotatedAt, &key.ExpiresAt, &key.RevokedAt, &key.LastUsedAt, &key.TokenHash,
	)
	if err == nil {
		_, _ = s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE id=$1`, key.ID)
	}
	return user, key, err
}

func (s *Store) ListAPIKeys(ctx context.Context, userID string) ([]model.APIKey, error) {
	query := `SELECT id,user_id,name,prefix,permissions,version,created_at,rotated_at,expires_at,revoked_at,last_used_at,token_hash FROM api_keys`
	args := []any{}
	if userID != "" {
		query += ` WHERE user_id=$1`
		args = append(args, userID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.APIKey, 0)
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, key)
	}
	return items, rows.Err()
}

func scanAPIKey(row pgx.Row) (model.APIKey, error) {
	var key model.APIKey
	err := row.Scan(&key.ID, &key.UserID, &key.Name, &key.Prefix, &key.Permissions, &key.Version, &key.CreatedAt, &key.RotatedAt, &key.ExpiresAt, &key.RevokedAt, &key.LastUsedAt, &key.TokenHash)
	return key, err
}

func (s *Store) CreateAPIKey(ctx context.Context, key model.APIKey, actorID string) (model.APIKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.APIKey{}, err
	}
	defer tx.Rollback(ctx)
	created, err := scanAPIKey(tx.QueryRow(ctx, `INSERT INTO api_keys(id,user_id,name,prefix,token_hash,permissions,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id,user_id,name,prefix,permissions,version,created_at,rotated_at,expires_at,revoked_at,last_used_at,token_hash`, key.ID, key.UserID, key.Name, key.Prefix, key.TokenHash, key.Permissions, key.ExpiresAt))
	if err != nil {
		return model.APIKey{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO api_key_rotations(id,key_id,version,prefix,rotated_by,reason) VALUES($1,$2,1,$3,$4,'created')`, secure.NewID("rot"), key.ID, key.Prefix, actorID)
	if err != nil {
		return model.APIKey{}, err
	}
	return created, tx.Commit(ctx)
}

func (s *Store) UpdateAPIKey(ctx context.Context, id, ownerID, name string, permissions []string) (model.APIKey, error) {
	return scanAPIKey(s.pool.QueryRow(ctx, `UPDATE api_keys SET name=$3,permissions=$4 WHERE id=$1 AND ($2='' OR user_id=$2) AND revoked_at IS NULL RETURNING id,user_id,name,prefix,permissions,version,created_at,rotated_at,expires_at,revoked_at,last_used_at,token_hash`, id, ownerID, name, permissions))
}

func (s *Store) RotateAPIKey(ctx context.Context, id, ownerID, tokenHash, prefix, actorID string) (model.APIKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.APIKey{}, err
	}
	defer tx.Rollback(ctx)
	key, err := scanAPIKey(tx.QueryRow(ctx, `UPDATE api_keys SET token_hash=$3,prefix=$4,version=version+1,rotated_at=now() WHERE id=$1 AND ($2='' OR user_id=$2) AND revoked_at IS NULL RETURNING id,user_id,name,prefix,permissions,version,created_at,rotated_at,expires_at,revoked_at,last_used_at,token_hash`, id, ownerID, tokenHash, prefix))
	if err != nil {
		return model.APIKey{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO api_key_rotations(id,key_id,version,prefix,rotated_by,reason) VALUES($1,$2,$3,$4,$5,'rotated')`, secure.NewID("rot"), key.ID, key.Version, key.Prefix, actorID); err != nil {
		return model.APIKey{}, err
	}
	return key, tx.Commit(ctx)
}

func (s *Store) RevokeAPIKey(ctx context.Context, id, ownerID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND ($2='' OR user_id=$2) AND revoked_at IS NULL`, id, ownerID)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}

type AuditEvent struct {
	ID, ActorID, Action, TargetType, TargetID, IP, UserAgent string
	Success                                                  bool
	Detail                                                   json.RawMessage
	CreatedAt                                                time.Time
}

func (s *Store) AddAudit(ctx context.Context, event AuditEvent) error {
	if len(event.Detail) == 0 {
		event.Detail = json.RawMessage(`{}`)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target_type,target_id,success,ip,user_agent,detail,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, event.ID, event.ActorID, event.Action, event.TargetType, event.TargetID, event.Success, event.IP, event.UserAgent, event.Detail, event.CreatedAt)
	return err
}

func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
func IsConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23503")
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
