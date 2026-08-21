ALTER TABLE mfa_login_challenges DROP COLUMN IF EXISTS client_ip;
ALTER TABLE mfa_login_challenges DROP COLUMN IF EXISTS user_agent;
ALTER TABLE sessions DROP COLUMN IF EXISTS client_ip;
ALTER TABLE sessions DROP COLUMN IF EXISTS user_agent;
