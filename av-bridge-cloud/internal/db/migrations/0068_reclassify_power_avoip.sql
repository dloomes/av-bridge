-- The power and avoip device categories (v1.0.64) only became the default
-- for new devices. This moves existing ones across: ATEN PDUs to power,
-- Aurora VPX encoders/decoders to avoip.
--
-- devices has FORCE ROW LEVEL SECURITY, and the migration role (the RDS
-- master) doesn't bypass it, so a plain UPDATE would see no rows. Lifting
-- FORCE inside this migration's transaction lets the table owner update
-- every tenant's devices; it is back on before the transaction commits.
--
-- type is the portal's (ingest never writes it) and isn't one of the
-- columns that bump a Collector's config_version, so this causes no resync.

ALTER TABLE devices NO FORCE ROW LEVEL SECURITY;

UPDATE devices SET type = 'power'
 WHERE protocol = 'aten_pdu' AND type IS DISTINCT FROM 'power';

UPDATE devices SET type = 'avoip'
 WHERE protocol = 'aurora_vpx' AND type IS DISTINCT FROM 'avoip';

ALTER TABLE devices FORCE ROW LEVEL SECURITY;
