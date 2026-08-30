-- Product-level ledger identities are deliberately separate from custody routes.
-- They remain disabled until treasury has approved an explicit consolidation path.
INSERT INTO assets (symbol, network, contract_address, decimals, status)
VALUES
    ('AAVE', 'internal_spot', '', 18, 'disabled'),
    ('ADA', 'internal_spot', '', 6, 'disabled'),
    ('ARB', 'internal_spot', '', 18, 'disabled'),
    ('AVAX', 'internal_spot', '', 18, 'disabled'),
    ('BNB', 'internal_spot', '', 18, 'disabled'),
    ('BTC', 'internal_spot', '', 8, 'disabled'),
    ('cNGN', 'internal_spot', '', 8, 'disabled'),
    ('DAI', 'internal_spot', '', 18, 'disabled'),
    ('ETH', 'internal_spot', '', 18, 'disabled'),
    ('LINK', 'internal_spot', '', 18, 'disabled'),
    ('OP', 'internal_spot', '', 18, 'disabled'),
    ('PEPE', 'internal_spot', '', 18, 'disabled'),
    ('POL', 'internal_spot', '', 18, 'disabled'),
    ('SHIB', 'internal_spot', '', 18, 'disabled'),
    ('SOL', 'internal_spot', '', 9, 'disabled'),
    ('stETH', 'internal_spot', '', 18, 'disabled'),
    ('TON', 'internal_spot', '', 9, 'disabled'),
    ('TRX', 'internal_spot', '', 6, 'disabled'),
    ('UNI', 'internal_spot', '', 18, 'disabled'),
    ('USDC', 'internal_spot', '', 6, 'disabled'),
    ('USDe', 'internal_spot', '', 18, 'disabled'),
    ('USDT', 'internal_spot', '', 6, 'disabled'),
    ('WBTC', 'internal_spot', '', 8, 'disabled'),
    ('WETH', 'internal_spot', '', 18, 'disabled'),
    ('XRP', 'internal_spot', '', 6, 'disabled')
ON CONFLICT (symbol, network, contract_address) DO NOTHING;

WITH desired(symbol, base_symbol, quantity_scale) AS (
    VALUES
        ('AAVEUSDT', 'AAVE', 18), ('ADAUSDT', 'ADA', 6),
        ('ARBUSDT', 'ARB', 18), ('AVAXUSDT', 'AVAX', 18),
        ('BNBUSDT', 'BNB', 18), ('BTCUSDT', 'BTC', 8),
        ('CNGNUSDT', 'cNGN', 8), ('DAIUSDT', 'DAI', 18),
        ('ETHUSDT', 'ETH', 18), ('LINKUSDT', 'LINK', 18),
        ('OPUSDT', 'OP', 18), ('PEPEUSDT', 'PEPE', 18),
        ('POLUSDT', 'POL', 18), ('SHIBUSDT', 'SHIB', 18),
        ('SOLUSDT', 'SOL', 9), ('STETHUSDT', 'stETH', 18),
        ('TONUSDT', 'TON', 9), ('TRXUSDT', 'TRX', 6),
        ('UNIUSDT', 'UNI', 18), ('USDCUSDT', 'USDC', 6),
        ('USDEUSDT', 'USDe', 18), ('WBTCUSDT', 'WBTC', 8),
        ('WETHUSDT', 'WETH', 18), ('XRPUSDT', 'XRP', 6)
)
INSERT INTO trading_pairs (
    symbol, base_asset_id, quote_asset_id, price_scale, quantity_scale,
    min_quantity_atomic, max_quantity_atomic, price_tick_atomic,
    quantity_step_atomic, status
)
SELECT desired.symbol, base.id, quote.id, 8, desired.quantity_scale,
       1, 18446744073709551615, 1, 1, 'halted'
FROM desired
JOIN assets base
  ON base.symbol = desired.base_symbol AND base.network = 'internal_spot'
JOIN assets quote
  ON quote.symbol = 'USDT' AND quote.network = 'internal_spot'
ON CONFLICT (symbol) DO NOTHING;

