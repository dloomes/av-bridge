package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Integration is a customer's connection to a vendor cloud (Poly Lens).
// SecretEnc is ciphertext from secrets.Cipher; it must never leave the
// server. All functions here run inside a tenant transaction.
type Integration struct {
	ID            string
	CustomerID    string
	Provider      string
	Status        string
	ClientID      string
	SecretEnc     []byte
	AccountID     string
	AccountName   string
	AllowTagging  bool
	LastSuccessAt *time.Time
	LastError     string
}

// GetIntegration returns the customer's integration for a provider, or nil.
func GetIntegration(ctx context.Context, tx pgx.Tx, provider string) (*Integration, error) {
	return getIntegration(ctx, tx, provider, "")
}

// GetIntegrationForUpdate is GetIntegration with the row locked until the
// transaction ends, for read-check-write sequences.
func GetIntegrationForUpdate(ctx context.Context, tx pgx.Tx, provider string) (*Integration, error) {
	return getIntegration(ctx, tx, provider, " FOR UPDATE")
}

func getIntegration(ctx context.Context, tx pgx.Tx, provider, lock string) (*Integration, error) {
	var i Integration
	err := tx.QueryRow(ctx, `
		SELECT id::text, customer_id::text, provider, status, COALESCE(client_id, ''), secret_enc,
		       COALESCE(account_id, ''), COALESCE(account_name, ''), allow_tagging,
		       last_success_at, COALESCE(last_error, '')
		  FROM integrations WHERE provider = $1`+lock, provider).
		Scan(&i.ID, &i.CustomerID, &i.Provider, &i.Status, &i.ClientID, &i.SecretEnc,
			&i.AccountID, &i.AccountName, &i.AllowTagging, &i.LastSuccessAt, &i.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

// ListIntegrations returns every integration of the customer with the number
// of linked devices.
func ListIntegrations(ctx context.Context, tx pgx.Tx) ([]Integration, map[string]int, error) {
	rows, err := tx.Query(ctx, `
		SELECT i.id::text, i.customer_id::text, i.provider, i.status, COALESCE(i.client_id, ''), i.secret_enc,
		       COALESCE(i.account_id, ''), COALESCE(i.account_name, ''), i.allow_tagging,
		       i.last_success_at, COALESCE(i.last_error, ''),
		       (SELECT count(*) FROM integration_devices d WHERE d.integration_id = i.id)
		  FROM integrations i ORDER BY i.provider`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []Integration
	linked := map[string]int{}
	for rows.Next() {
		var i Integration
		var n int
		if err := rows.Scan(&i.ID, &i.CustomerID, &i.Provider, &i.Status, &i.ClientID, &i.SecretEnc,
			&i.AccountID, &i.AccountName, &i.AllowTagging, &i.LastSuccessAt, &i.LastError, &n); err != nil {
			return nil, nil, err
		}
		linked[i.Provider] = n
		out = append(out, i)
	}
	return out, linked, rows.Err()
}

// UpsertIntegration connects or updates the customer's integration. A nil
// SecretEnc keeps the stored secret and the current status and last error
// (a settings-only change proves nothing about the connection); a new secret
// has been verified against Lens, so it marks the integration connected and
// clears the last error. Returns the integration id and whether it was new.
func UpsertIntegration(ctx context.Context, tx pgx.Tx, customerID string, in Integration, actor string) (string, bool, error) {
	var id string
	var created bool
	err := tx.QueryRow(ctx, `
		INSERT INTO integrations (customer_id, provider, status, client_id, secret_enc, account_id, account_name,
		                          allow_tagging, last_success_at, created_by, updated_by)
		VALUES ($1, $2, 'connected', $3, $4, $5, $6, $7, now(), $8, $8)
		ON CONFLICT (customer_id, provider) DO UPDATE SET
		    status = CASE WHEN EXCLUDED.secret_enc IS NOT NULL THEN 'connected' ELSE integrations.status END,
		    client_id = EXCLUDED.client_id,
		    secret_enc = COALESCE(EXCLUDED.secret_enc, integrations.secret_enc),
		    account_id = EXCLUDED.account_id,
		    account_name = EXCLUDED.account_name,
		    allow_tagging = EXCLUDED.allow_tagging,
		    last_success_at = CASE WHEN EXCLUDED.secret_enc IS NOT NULL THEN now() ELSE integrations.last_success_at END,
		    last_error = CASE WHEN EXCLUDED.secret_enc IS NOT NULL THEN NULL ELSE integrations.last_error END,
		    updated_at = now(),
		    updated_by = EXCLUDED.updated_by
		RETURNING id::text, (xmax = 0)`,
		customerID, in.Provider, in.ClientID, in.SecretEnc, in.AccountID, in.AccountName, in.AllowTagging, actor).
		Scan(&id, &created)
	return id, created, err
}

// DisconnectIntegration wipes the stored secret and marks the integration
// disconnected. Linked devices are kept. Returns false when none exists.
func DisconnectIntegration(ctx context.Context, tx pgx.Tx, provider, actor string) (string, bool, error) {
	var id string
	err := tx.QueryRow(ctx, `
		UPDATE integrations SET status = 'disconnected', secret_enc = NULL, updated_at = now(), updated_by = $2
		 WHERE provider = $1 RETURNING id::text`, provider, actor).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// MarkIntegrationError records a failed call to the vendor cloud.
func MarkIntegrationError(ctx context.Context, tx pgx.Tx, id, msg string) error {
	_, err := tx.Exec(ctx, `UPDATE integrations SET status = 'error', last_error = $2, updated_at = now() WHERE id = $1`, id, msg)
	return err
}

// MarkIntegrationOK records a successful call to the vendor cloud.
func MarkIntegrationOK(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE integrations SET status = 'connected', last_success_at = now(), last_error = NULL WHERE id = $1`, id)
	return err
}

// IntegrationLinks maps external id to linked device id for an integration.
func IntegrationLinks(ctx context.Context, tx pgx.Tx, integrationID string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT external_id, device_id::text FROM integration_devices WHERE integration_id = $1`, integrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var ext, dev string
		if err := rows.Scan(&ext, &dev); err != nil {
			return nil, err
		}
		out[ext] = dev
	}
	return out, rows.Err()
}

// CollectorDeviceIdent is a Collector device's identity for matching Lens rows.
type CollectorDeviceIdent struct {
	ID, Name, Serial, MAC string
}

// CollectorDeviceIdents lists the customer's Collector devices that have a
// serial number or MAC address.
func CollectorDeviceIdents(ctx context.Context, tx pgx.Tx) ([]CollectorDeviceIdent, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, COALESCE(name, ''), COALESCE(serial_number, ''), COALESCE(mac_address, '')
		  FROM devices
		 WHERE source = 'collector' AND (COALESCE(serial_number, '') <> '' OR COALESCE(mac_address, '') <> '')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CollectorDeviceIdent
	for rows.Next() {
		var d CollectorDeviceIdent
		if err := rows.Scan(&d.ID, &d.Name, &d.Serial, &d.MAC); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// HasNotificationChannelType reports whether the customer has a channel of the type.
func HasNotificationChannelType(ctx context.Context, tx pgx.Tx, typ string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notification_channels WHERE type = $1)`, typ).Scan(&ok)
	return ok, err
}
