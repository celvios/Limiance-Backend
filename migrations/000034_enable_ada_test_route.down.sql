UPDATE assets
SET status = 'disabled'
WHERE symbol = 'ADA'
  AND network = 'cardano_testnet'
  AND custody_asset_id = 'ADA_TEST';
