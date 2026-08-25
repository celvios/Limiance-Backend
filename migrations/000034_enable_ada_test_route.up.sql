-- Enable ADA only after its Fireblocks asset mapping was verified.
UPDATE assets
SET status = 'enabled'
WHERE symbol = 'ADA'
  AND network = 'cardano_testnet'
  AND custody_asset_id = 'ADA_TEST';
