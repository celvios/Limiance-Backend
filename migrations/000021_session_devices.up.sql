ALTER TABLE sessions
    ADD COLUMN user_agent TEXT NOT NULL DEFAULT '',
    ADD COLUMN client_ip INET;

ALTER TABLE mfa_login_challenges
    ADD COLUMN user_agent TEXT NOT NULL DEFAULT '',
    ADD COLUMN client_ip INET;
