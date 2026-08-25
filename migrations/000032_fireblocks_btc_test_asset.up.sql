-- The staging Fireblocks workspace exposes the funded Bitcoin test asset as BTC_TEST.
-- The provider vault endpoint expects the legacy asset ID, not the registry UUID.
UPDATE assets
SET custody_asset_id = 'BTC_TEST'
WHERE symbol = 'BTC'
  AND network = 'bitcoin_testnet4';
