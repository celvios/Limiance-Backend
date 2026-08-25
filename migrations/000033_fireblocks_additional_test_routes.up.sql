-- Register Fireblocks Sandbox test routes without enabling address generation.
-- Each route must pass provider address and webhook tests before activation.
INSERT INTO network_catalog (code, display_name, status) VALUES
    ('cardano_testnet', 'Cardano Testnet', 'approved_pending_custody_enablement'),
    ('bitcoin_cash_testnet', 'Bitcoin Cash Testnet', 'approved_pending_custody_enablement'),
    ('cosmos_testnet', 'Cosmos Hub Testnet', 'approved_pending_custody_enablement'),
    ('avalanche_fuji', 'Avalanche Fuji', 'approved_pending_custody_enablement'),
    ('dogecoin_testnet', 'Dogecoin Testnet', 'approved_pending_custody_enablement'),
    ('polygon_amoy', 'Polygon Amoy', 'approved_pending_custody_enablement'),
    ('solana_testnet', 'Solana Testnet', 'approved_pending_custody_enablement'),
    ('tron_testnet', 'TRON Testnet', 'approved_pending_custody_enablement'),
    ('bnb_testnet', 'BNB Smart Chain Testnet', 'approved_pending_custody_enablement'),
    ('bitcoin_test', 'Bitcoin Testnet', 'approved_pending_custody_enablement')
ON CONFLICT (code) DO NOTHING;

INSERT INTO assets (symbol, network, contract_address, decimals, status, confirmations_required, custody_asset_id) VALUES
    ('ADA', 'cardano_testnet', '', 6, 'disabled', 1, 'ADA_TEST'),
    ('BCH', 'bitcoin_cash_testnet', '', 8, 'disabled', 1, 'BCH_TEST'),
    ('ATOM', 'cosmos_testnet', '', 6, 'disabled', 1, 'ATOM_COS_TEST'),
    ('AVAX', 'avalanche_fuji', '', 18, 'disabled', 1, 'AVAXTEST'),
    ('DOGETEST', 'dogecoin_testnet', '', 8, 'disabled', 1, 'DOGE_TEST'),
    ('POL', 'polygon_amoy', '', 18, 'disabled', 1, 'AMOY_POLYGON_TEST'),
    ('SOL', 'solana_testnet', '', 9, 'disabled', 1, 'SOL_TEST'),
    ('TRX', 'tron_testnet', '', 6, 'disabled', 1, 'TRX_TEST'),
    ('BNB', 'bnb_testnet', '', 18, 'disabled', 1, 'BNB_TEST'),
    ('BTC', 'bitcoin_test', '', 8, 'disabled', 1, 'BTC_TEST')
ON CONFLICT (symbol, network, contract_address) DO UPDATE
SET decimals = EXCLUDED.decimals,
    status = 'disabled',
    confirmations_required = EXCLUDED.confirmations_required,
    custody_asset_id = EXCLUDED.custody_asset_id;
