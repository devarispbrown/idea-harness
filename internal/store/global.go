package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/devarispbrown/idea-harness/internal/id"
)

// ProjectRecord is a registered project in global.db.
type ProjectRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	RootPath  string    `json:"root_path"`
	ProjectID string    `json:"project_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RegisterProject inserts or updates a project registry entry and returns the
// project id. The global registry has no event log, so this is a direct upsert.
func (db *DB) RegisterProject(ctx context.Context, name, rootPath, projectID string) (string, error) {
	if projectID == "" {
		projectID = id.New()
	}
	_, err := db.SQL().ExecContext(ctx, `
		INSERT INTO projects (id, name, root_path, project_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET root_path = excluded.root_path, updated_at = excluded.updated_at`,
		projectID, name, rootPath, projectID, time.Now().UTC().UnixMilli(), time.Now().UTC().UnixMilli())
	if err != nil {
		return "", err
	}
	return projectID, nil
}

// ListProjects returns all registered projects.
func (db *DB) ListProjects(ctx context.Context) ([]ProjectRecord, error) {
	rows, err := db.SQL().QueryContext(ctx, `SELECT id, name, root_path, project_id, created_at, updated_at FROM projects ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ProjectRecord, 0)
	for rows.Next() {
		var p ProjectRecord
		var created, updated int64
		if err := rows.Scan(&p.ID, &p.Name, &p.RootPath, &p.ProjectID, &created, &updated); err != nil {
			return nil, err
		}
		p.CreatedAt = time.UnixMilli(created)
		p.UpdatedAt = time.UnixMilli(updated)
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject returns a project by its project_id.
func (db *DB) GetProject(ctx context.Context, projectID string) (ProjectRecord, error) {
	var p ProjectRecord
	var created, updated int64
	err := db.SQL().QueryRowContext(ctx, `SELECT id, name, root_path, project_id, created_at, updated_at FROM projects WHERE project_id = ?`, projectID).
		Scan(&p.ID, &p.Name, &p.RootPath, &p.ProjectID, &created, &updated)
	if err != nil {
		return p, notFoundIfNoRows(err, "project", projectID)
	}
	p.CreatedAt = time.UnixMilli(created)
	p.UpdatedAt = time.UnixMilli(updated)
	return p, nil
}

// SetDaemonMeta stores a daemon-wide key/value in global.db; an empty value
// deletes the key.
func (db *DB) SetDaemonMeta(ctx context.Context, key, value string) error {
	if value == "" {
		_, err := db.SQL().ExecContext(ctx, `DELETE FROM daemon_meta WHERE key = ?`, key)
		return err
	}
	_, err := db.SQL().ExecContext(ctx, `INSERT INTO daemon_meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DaemonMeta returns a daemon-wide value ("" when unset).
func (db *DB) DaemonMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := db.SQL().QueryRowContext(ctx, `SELECT value FROM daemon_meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}
