-- Remote access: the page a session opens on, for devices whose web
-- interface doesn't start at "/" (e.g. /login). Chosen when the session is
-- started, defaulting to the device's web_path tag.

ALTER TABLE remote_session
    ADD COLUMN IF NOT EXISTS start_path text NOT NULL DEFAULT '/'
        CHECK (start_path LIKE '/%' AND start_path NOT LIKE '//%');
