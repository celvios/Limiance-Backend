-- Fireblocks vault asset-wallet endpoints require the provider asset UUID.
-- BTC_TEST4 remains the provider legacy ID exposed by the asset registry.
UPDATE assets
SET custody_asset_id = '7bac3607-f372-4298-b358-6a1358a1628f'
WHERE symbol = 'BTC'
  AND network = 'bitcoin_testnet4'
  AND custody_asset_id = 'BTC_TEST4';
