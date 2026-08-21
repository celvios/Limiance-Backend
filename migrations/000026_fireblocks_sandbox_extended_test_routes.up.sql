-- Verified against the configured Fireblocks Sandbox /v1/assets registry on
-- 2026-08-21. These are explicitly testnet routes. Do not map a Sandbox asset
-- to a mainnet network code: that would present a misleading customer route.
INSERT INTO network_catalog (code, display_name, status) VALUES
    ('arbitrum_sepolia', 'Arbitrum Sepolia', 'approved_pending_custody_enablement'),
    ('base_sepolia', 'Base Sepolia', 'approved_pending_custody_enablement'),
    ('optimism_sepolia', 'Optimism Sepolia', 'approved_pending_custody_enablement')
ON CONFLICT (code) DO NOTHING;

INSERT INTO assets (
    symbol, network, contract_address, decimals, status,
    confirmations_required, custody_asset_id
) VALUES
    ('USDC', 'ethereum_sepolia', '', 6, 'enabled', 2, 'USDC_ETH_TEST5_AN74'),
    ('ETH', 'arbitrum_sepolia', '', 18, 'enabled', 2, 'ETH-AETH_SEPOLIA'),
    ('ETH', 'base_sepolia', '', 18, 'enabled', 2, 'BASECHAIN_ETH_TEST5'),
    ('ETH', 'optimism_sepolia', '', 18, 'enabled', 2, 'ETH-OPT_SEPOLIA')
ON CONFLICT (symbol, network, contract_address) DO UPDATE
SET decimals = EXCLUDED.decimals,
    status = EXCLUDED.status,
    confirmations_required = EXCLUDED.confirmations_required,
    custody_asset_id = EXCLUDED.custody_asset_id;

-- An earlier development route used a hyphenated name. It has no active
-- deposit address and must remain disabled so Sepolia has one canonical code.
UPDATE assets
SET status = 'disabled'
WHERE symbol = 'ETH'
  AND network = 'ethereum-sepolia'
  AND contract_address = '';
