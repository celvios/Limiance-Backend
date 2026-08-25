ALTER TABLE users
    ADD COLUMN display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN preferred_currency CHAR(3) NOT NULL DEFAULT 'USD',
    ADD COLUMN preferred_language TEXT NOT NULL DEFAULT 'en',
    ADD COLUMN preferred_theme TEXT NOT NULL DEFAULT 'system';

ALTER TABLE users
    ADD CONSTRAINT users_preferred_currency_check CHECK (preferred_currency ~ '^[A-Z]{3}$'),
    ADD CONSTRAINT users_preferred_theme_check CHECK (preferred_theme IN ('system','light','dark'));
