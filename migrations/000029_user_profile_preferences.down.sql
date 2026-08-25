ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_preferred_currency_check,
    DROP CONSTRAINT IF EXISTS users_preferred_theme_check,
    DROP COLUMN IF EXISTS display_name,
    DROP COLUMN IF EXISTS preferred_currency,
    DROP COLUMN IF EXISTS preferred_language,
    DROP COLUMN IF EXISTS preferred_theme;
