DELETE FROM trading_pairs pair
USING assets base, assets quote
WHERE pair.base_asset_id = base.id
  AND pair.quote_asset_id = quote.id
  AND base.network = 'internal_spot'
  AND quote.network = 'internal_spot'
  AND pair.symbol IN (
    'AAVEUSDT','ADAUSDT','ARBUSDT','AVAXUSDT','BNBUSDT','BTCUSDT',
    'CNGNUSDT','DAIUSDT','ETHUSDT','LINKUSDT','OPUSDT','PEPEUSDT',
    'POLUSDT','SHIBUSDT','SOLUSDT','STETHUSDT','TONUSDT','TRXUSDT',
    'UNIUSDT','USDCUSDT','USDEUSDT','WBTCUSDT','WETHUSDT','XRPUSDT'
  );

DELETE FROM assets
WHERE network = 'internal_spot'
  AND symbol IN (
      'AAVE','ADA','ARB','AVAX','BNB','BTC','cNGN','DAI','ETH','LINK','OP',
      'PEPE','POL','SHIB','SOL','stETH','TON','TRX','UNI','USDC','USDe',
      'USDT','WBTC','WETH','XRP'
  );
