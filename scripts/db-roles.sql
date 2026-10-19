\set ON_ERROR_STOP on
BEGIN;

-- Run as the bootstrap administrator, in this application's database only.
SELECT 'CREATE ROLE weather_migrator LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'weather_migrator')
\gexec
SELECT 'CREATE ROLE weather_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS'
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'weather_app')
\gexec

-- Refuse to reuse privileged roles instead of silently changing existing access.
DO $$
BEGIN
    IF EXISTS (
        SELECT FROM pg_roles
        WHERE rolname IN ('weather_migrator', 'weather_app')
          AND (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)
    ) OR EXISTS (
        SELECT FROM pg_auth_members
        WHERE member IN (SELECT oid FROM pg_roles WHERE rolname IN ('weather_migrator', 'weather_app'))
    ) THEN
        RAISE EXCEPTION 'Application roles must be unprivileged and have no role memberships';
    END IF;
END
$$;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
SELECT format('REVOKE CREATE ON DATABASE %I FROM PUBLIC', current_database())
\gexec
GRANT USAGE, CREATE ON SCHEMA public TO weather_migrator;
GRANT USAGE ON SCHEMA public TO weather_app;
SELECT format('GRANT CONNECT ON DATABASE %I TO weather_migrator, weather_app', current_database())
\gexec
COMMIT;

-- Assign login passwords with make db-passwords, never as plaintext SQL.
