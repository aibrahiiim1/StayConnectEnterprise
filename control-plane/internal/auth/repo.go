package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrOperatorNotFound = errors.New("operator not found")

type Operator struct {
	ID            string
	Email         string
	DisplayName   string
	PasswordHash  string
	Status        string
	DefaultTenant string   // first non-NULL tenant_id in operator_roles, or ""
	Roles         []string // role strings (e.g. platform_admin, tenant_admin)
	IsSuperAdmin  bool
}

type Repo struct {
	DB *pgxpool.Pool
}

func (r *Repo) FindByEmail(ctx context.Context, email string) (*Operator, error) {
	var op Operator
	err := r.DB.QueryRow(ctx, `
        SELECT id, email, COALESCE(display_name,''), COALESCE(password_hash,''), status
          FROM operators
         WHERE lower(email) = lower($1)
    `, email).Scan(&op.ID, &op.Email, &op.DisplayName, &op.PasswordHash, &op.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOperatorNotFound
		}
		return nil, err
	}
	if err := r.loadRoles(ctx, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (r *Repo) FindByID(ctx context.Context, id string) (*Operator, error) {
	var op Operator
	err := r.DB.QueryRow(ctx, `
        SELECT id, email, COALESCE(display_name,''), COALESCE(password_hash,''), status
          FROM operators
         WHERE id = $1
    `, id).Scan(&op.ID, &op.Email, &op.DisplayName, &op.PasswordHash, &op.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrOperatorNotFound
		}
		return nil, err
	}
	if err := r.loadRoles(ctx, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (r *Repo) loadRoles(ctx context.Context, op *Operator) error {
	rows, err := r.DB.Query(ctx, `
        SELECT role, COALESCE(tenant_id::text,'')
          FROM operator_roles
         WHERE operator_id = $1
         ORDER BY role
    `, op.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var firstTenant string
	for rows.Next() {
		var role, tenID string
		if err := rows.Scan(&role, &tenID); err != nil {
			return err
		}
		op.Roles = append(op.Roles, role)
		if role == "platform_owner" || role == "platform_admin" {
			op.IsSuperAdmin = true
		}
		if tenID != "" && firstTenant == "" {
			firstTenant = tenID
		}
	}
	op.DefaultTenant = firstTenant
	return rows.Err()
}
