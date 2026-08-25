-- Enable the additional Fireblocks Sandbox routes after catalog verification.
UPDATE network_catalog
SET status = 'enabled'
WHERE code IN (
    'cardano_testnet',
    'bitcoin_cash_testnet',
    'cosmos_testnet',
    'avalanche_fuji',
    'dogecoin_testnet',
    'polygon_amoy',
    'solana_testnet',
    'tron_testnet',
    'bnb_testnet',
    'bitcoin_test'
);

UPDATE assets
SET status = 'enabled'
WHERE (symbol, network, contract_address) IN (
    ('ADA', 'cardano_testnet', ''),
    ('BCH', 'bitcoin_cash_testnet', ''),
    ('ATOM', 'cosmos_testnet', ''),
    ('AVAX', 'avalanche_fuji', ''),
    ('DOGETEST', 'dogecoin_testnet', ''),
    ('POL', 'polygon_amoy', ''),
    ('SOL', 'solana_testnet', ''),
    ('TRX', 'tron_testnet', ''),
    ('BNB', 'bnb_testnet', ''),
    ('BTC', 'bitcoin_test', '')
);
