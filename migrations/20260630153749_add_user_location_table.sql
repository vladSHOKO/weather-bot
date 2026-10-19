-- +goose Up
CREATE TABLE user_locations
(
    chat_id       BIGINT PRIMARY KEY,
    location_name TEXT             NOT NULL,
    latitude      DOUBLE PRECISION NOT NULL,
    longitude     DOUBLE PRECISION NOT NULL,
    timezone      TEXT             NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS user_locations;
