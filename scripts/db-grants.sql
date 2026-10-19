\set ON_ERROR_STOP on
-- Explicit table grants keep goose's schema-version table inaccessible to the bot.
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.user_locations TO weather_app;

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.bot_state, public.bot_updates TO weather_app;
GRANT SELECT ON TABLE public.bot_schema_version TO weather_app;
