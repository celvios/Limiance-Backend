CREATE TABLE asset_catalog (
    symbol TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'approved_pending_network_enablement',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO asset_catalog (symbol, display_name) VALUES
    ('BTC', 'Bitcoin'), ('ETH', 'Ethereum'), ('USDT', 'Tether'), ('USDC', 'USD Coin'), ('cNGN', 'Compliant Nigerian Naira'),
    ('TRX', 'TRON'), ('BNB', 'BNB'), ('SOL', 'Solana'), ('XRP', 'XRP'), ('ADA', 'Cardano'), ('TON', 'Toncoin'),
    ('AVAX', 'Avalanche'), ('POL', 'Polygon'), ('ARB', 'Arbitrum'), ('OP', 'Optimism'), ('LINK', 'Chainlink'),
    ('AAVE', 'Aave'), ('UNI', 'Uniswap'), ('DAI', 'Dai'), ('USDe', 'Ethena USDe'), ('WBTC', 'Wrapped Bitcoin'),
    ('WETH', 'Wrapped Ether'), ('stETH', 'Lido Staked ETH'), ('SHIB', 'Shiba Inu'), ('PEPE', 'Pepe')
ON CONFLICT (symbol) DO NOTHING;

CREATE TABLE network_catalog (
    code TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'approved_pending_custody_enablement',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO network_catalog (code, display_name) VALUES
    ('bitcoin', 'Bitcoin'), ('ethereum', 'Ethereum'), ('tron', 'TRON'), ('bnb_smart_chain', 'BNB Smart Chain'),
    ('solana', 'Solana'), ('xrp_ledger', 'XRP Ledger'), ('ton', 'TON'), ('avalanche_c_chain', 'Avalanche C-Chain'),
    ('polygon_pos', 'Polygon PoS'), ('arbitrum_one', 'Arbitrum One'), ('base', 'Base'), ('optimism', 'Optimism'), ('cardano', 'Cardano')
ON CONFLICT (code) DO NOTHING;
