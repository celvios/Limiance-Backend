ALTER TABLE custody_wallets DROP CONSTRAINT custody_wallets_user_provider_unique;
ALTER TABLE custody_wallets ADD CONSTRAINT custody_wallets_user_id_key UNIQUE (user_id);
