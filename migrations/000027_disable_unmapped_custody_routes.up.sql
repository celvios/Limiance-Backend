-- An enabled route without a provider asset ID cannot issue a custody address.
-- Keep it disabled until a verified Fireblocks production asset mapping exists.
UPDATE assets
SET status = 'disabled'
WHERE symbol = 'USDT'
  AND network = 'ethereum'
  AND custody_asset_id = '';
