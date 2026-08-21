# Self-custody testnet IAM boundary

These policies are templates, not deploy commands. Replace every placeholder
with the ARN created in the Limiance AWS account in `eu-central-1`.

- Attach `self-custody-testnet-signer-policy.json` only to the isolated signer
  ECS task role. Its key policy must also allow that role.
- Attach `api-custody-deny-policy.json` to the public API ECS task role. This
  explicit deny protects against accidental future policy grants.
- The signer task security group accepts HTTPS only from the API task security
  group. It has no public IP and no public load balancer.
- Enforce mTLS at the internal load balancer/service mesh. The signer must
  reject requests without the API client certificate.
- Do not attach either policy to local developer credentials.

The testnet KMS key must be an asymmetric `ECC_SECG_P256K1`, `SIGN_VERIFY`
customer-managed key. It is not a replacement for the audited MPC/HD wallet
engine required before production customer custody.
