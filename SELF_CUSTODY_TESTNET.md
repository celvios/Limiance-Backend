# Limiance self-custody testnet pilot

This is a testnet-only custody track. It cannot be enabled for a mainnet
network by configuration alone.

## Boundary

The customer API has no signing permission, no private key, and no direct
chain-node credential. It calls the isolated signer at
`SELF_CUSTODY_SIGNER_URL` over mTLS. The signer is the only workload permitted
to call the selected signing backend.

## AWS resources, eu-central-1

Create an asymmetric customer-managed KMS key with:

- Key usage: `SIGN_VERIFY`
- Key spec: `ECC_SECG_P256K1`
- Alias: `alias/limiance-testnet-ethereum-signer`
- Rotation and deletion protection governed by the key policy

AWS documents `ECC_SECG_P256K1` for cryptocurrency use. KMS keeps the private
key inside KMS and returns DER-encoded ECDSA signatures; the signer service is
responsible for Ethereum's required signature conversion and recovery ID.

KMS alone does not provide HD child-key derivation. It is suitable for the
first protected testnet signing backend, but the production wallet engine must
use an audited MPC/HSM design that provides per-customer derivation and
threshold signing across all supported networks. The API-to-signer contract is
unchanged when that backend replaces the testnet implementation.

Create a distinct symmetric KMS key for data encryption. Do not reuse a signing
key for encryption.

## IAM split

- **API ECS task role:** no `kms:Sign`, no `kms:GetPublicKey` for the signing
  key; may call only the signer over the private network.
- **Signer ECS task role:** `kms:Sign` restricted to
  `ECDSA_SHA_256`, `kms:GetPublicKey`, and CloudWatch logging only.
- **Operations break-glass role:** separate, MFA-protected, time-limited.

## Enablement sequence

1. Deploy signer and API to private subnets with mTLS and security-group-only
   access from API to signer.
2. Verify the signer key is `ECC_SECG_P256K1` and `SIGN_VERIFY`.
   Run `go run ./cmd/kms-preflight` from the signer deployment role; it must
   print `kms_testnet_signer_preflight=passed` before any signer rollout.
3. Set `CUSTODY_MODE=self_custody_testnet` and
   `SELF_CUSTODY_TESTNET_ENABLED=true` only in staging.
4. Set `SELF_CUSTODY_SIGNER_URL` to the internal HTTPS signer URL.
5. Enable only `ETH` / `ethereum-sepolia` in the asset registry.
6. Run address issuance, deposit observation, withdrawal policy, kill-switch,
   and recovery tests. No mainnet route may be enabled.

The signer API contract is:

- `POST /v1/testnet/wallets` → `{ "id": "opaque-wallet-id" }`
- `POST /v1/testnet/wallets/{wallet_id}/addresses` with `asset_id` and
  `idempotency_key` → `{ "id": "opaque-address-id", "address": "...", "tag": "" }`

Signing and broadcast APIs are intentionally absent until the address and
deposit workflows, audit controls, and independent security review pass.
