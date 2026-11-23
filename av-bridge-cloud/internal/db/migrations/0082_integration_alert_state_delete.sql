-- Deleting a Lens-managed device (a soft delete, so no FK cascade) clears its
-- alert-state rows in the same transaction as the tenant API.
GRANT DELETE ON integration_alert_state TO app_tenant;
