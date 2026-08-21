ALTER TABLE custody_wallets DROP CONSTRAINT custody_wallets_user_id_key;
ALTER TABLE custody_wallets ADD CONSTRAINT custody_wallets_user_provider_unique UNIQUE (user_id, provider);
