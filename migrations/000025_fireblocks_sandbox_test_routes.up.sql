-- Verified against the configured Fireblocks Sandbox workspace through the
-- authenticated /v1/assets registry on 2026-08-21. These are testnet-only
-- routes; production networks remain disabled until separately approved.
INSERT INTO network_catalog (code, display_name, status) VALUES
    ('bitcoin_testnet4', 'Bitcoin Testnet4', 'approved_pending_custody_enablement'),
    ('ethereum_sepolia', 'Ethereum Sepolia', 'approved_pending_custody_enablement')
ON CONFLICT (code) DO NOTHING;

INSERT INTO assets (
    symbol, network, contract_address, decimals, status,
    confirmations_required, custody_asset_id
) VALUES
    ('BTC', 'bitcoin_testnet4', '', 8, 'enabled', 2, 'BTC_TEST4'),
    ('ETH', 'ethereum_sepolia', '', 18, 'enabled', 2, 'ETH_TEST5')
ON CONFLICT (symbol, network, contract_address) DO UPDATE
SET decimals = EXCLUDED.decimals,
    status = EXCLUDED.status,
    confirmations_required = EXCLUDED.confirmations_required,
    custody_asset_id = EXCLUDED.custody_asset_id;
