-- 0039_buildings_geocoords.sql — geographic coordinates on buildings.
--
-- The overview map needs a lat/lon per building to plot pins. Coords are
-- optional (nullable) so an existing building without a pin still renders
-- in the fallback tile grid; only buildings with both coords populated
-- appear on the map. Stored as double precision — Postgres's native
-- floating-point is plenty for the ~1m precision Mapbox needs at any
-- zoom level a building would show at.
--
-- No PostGIS. All the queries we need (list buildings for the map, filter
-- by bounding box in the future) fit into plain SELECTs. Introducing an
-- extension for two columns is not worth the migration risk.

ALTER TABLE buildings
    ADD COLUMN latitude  double precision,
    ADD COLUMN longitude double precision,
    ADD CONSTRAINT buildings_lat_lon_range CHECK (
        (latitude IS NULL AND longitude IS NULL)
        OR (
            latitude  BETWEEN -90  AND 90
            AND longitude BETWEEN -180 AND 180
        )
    );
