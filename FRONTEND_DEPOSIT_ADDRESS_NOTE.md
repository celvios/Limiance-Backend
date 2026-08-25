# Frontend Deposit Address Note

## Current status

The staging API successfully generates the BTC Testnet4 deposit address. The frontend must display both the QR code and the returned address text.

## Request

```http
POST https://api.staging.celvios.site/v1/wallet/deposit-addresses
```

```json
{
  "asset_symbol": "BTC",
  "network": "bitcoin_testnet4"
}
```

Use the shared API helper with `credentials: "include"`.

## Response fields

The successful response includes these fields:

```json
{
  "id": "...",
  "asset_symbol": "BTC",
  "network": "bitcoin_testnet4",
  "address": "tb1...",
  "tag": "",
  "provider_address_id": "...",
  "status": "active"
}
```

Render `response.address` as selectable text next to the QR code and use the same value for the QR component. Render `response.tag` when it is non-empty.

```tsx
<QRCode value={depositAddress.address} />
<code>{depositAddress.address}</code>
```

Do not display a placeholder when `address` is present. Do not convert the address to a number or truncate it in the copied value. The copy action should copy the full address.

## Supported route examples

- BTC: `bitcoin_testnet4`
- ETH: `ethereum_sepolia`, `arbitrum_sepolia`, `base_sepolia`, `optimism_sepolia`
- USDC: `ethereum_sepolia`

The UI should display the exact selected asset and network beside the address, and must not reuse an address from another route.

## Error handling

- `401`: refresh authentication state.
- `409`: disable the selected asset/network route; do not retry automatically.
- `503`: show provider unavailable and allow manual retry.

Do not expose cookies, API keys, private keys, or authorization headers in bug reports or screenshots.
