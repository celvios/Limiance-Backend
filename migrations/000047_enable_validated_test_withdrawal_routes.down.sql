UPDATE assets
SET status = 'disabled'
WHERE (symbol, network, custody_asset_id) IN (
    ('SOL', 'solana_testnet', 'SOL_TEST'),
    ('POL', 'polygon_amoy', 'AMOY_POLYGON_TEST'),
    ('TRX', 'tron_testnet', 'TRX_TEST'),
    ('BNB', 'bnb_testnet', 'BNB_TEST'),
    ('ADA', 'cardano_testnet', 'ADA_TEST'),
    ('BCH', 'bitcoin_cash_testnet', 'BCH_TEST'),
    ('ATOM', 'cosmos_testnet', 'ATOM_COS_TEST'),
    ('AVAX', 'avalanche_fuji', 'AVAXTEST'),
    ('DOGETEST', 'dogecoin_testnet', 'DOGE_TEST'),
    ('BTC', 'bitcoin_test', 'BTC_TEST')
);

UPDATE network_catalog
SET status = 'approved_pending_custody_enablement'
WHERE code IN (
    'solana_testnet', 'polygon_amoy', 'tron_testnet', 'bnb_testnet',
    'cardano_testnet', 'bitcoin_cash_testnet', 'cosmos_testnet',
    'avalanche_fuji', 'dogecoin_testnet', 'bitcoin_test'
);
