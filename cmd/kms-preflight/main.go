// kms-preflight verifies that the isolated signer role is bound to the exact
// asymmetric cryptocurrency key expected by the Limiance testnet custody path.
// It never signs a message and never prints key material.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/limiance/backend/internal/config"
)

func main() {
	cfg := config.Load()
	if cfg.SelfCustodyKMSKeyID == "" {
		log.Fatal("SELF_CUSTODY_KMS_KEY_ID is required")
	}
	ctx := context.Background()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		log.Fatal(err)
	}
	client := kms.NewFromConfig(awsCfg)
	description, err := client.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: &cfg.SelfCustodyKMSKeyID})
	if err != nil {
		log.Fatal(err)
	}
	if description.KeyMetadata == nil || string(description.KeyMetadata.KeySpec) != "ECC_SECG_P256K1" || string(description.KeyMetadata.KeyUsage) != "SIGN_VERIFY" || string(description.KeyMetadata.KeyState) != "Enabled" {
		log.Fatal("KMS key must be enabled ECC_SECG_P256K1 with SIGN_VERIFY usage")
	}
	publicKey, err := client.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: &cfg.SelfCustodyKMSKeyID})
	if err != nil {
		log.Fatal(err)
	}
	allowed := false
	for _, algorithm := range publicKey.SigningAlgorithms {
		if string(algorithm) == "ECDSA_SHA_256" {
			allowed = true
		}
	}
	if !allowed {
		log.Fatal("KMS key does not permit ECDSA_SHA_256")
	}
	fmt.Fprintln(os.Stdout, "kms_testnet_signer_preflight=passed")
}
