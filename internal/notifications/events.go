package notifications

// RoutedEventTypes is the explicit allow-list of outbox events consumed by the
// notification worker. Keeping this list in the notifications boundary makes
// an unrecognised event operationally visible in the router quarantine queue
// rather than silently dropping it.
var RoutedEventTypes = []string{
	"email.verification_requested",
	"email.password_reset_requested",
	"deposit.submitted",
	"deposit.confirming",
	"deposit.credited",
	"deposit.failed",
	"withdrawal.submitted",
	"withdrawal.under_review",
	"withdrawal.approved",
	"withdrawal.completed",
	"withdrawal.rejected",
	"transfer.completed",
	"security.new_device_login",
	"security.password_changed",
	"security.2fa_enabled",
	"security.2fa_disabled",
	"security.api_key_created",
	"security.anti_phishing_code_enabled",
	"security.anti_phishing_code_cleared",
	"security.withdrawal_address_whitelisted",
}

// IsRoutedEvent reports whether an outbox event belongs on the notifications
// queue. It is deliberately an allow-list: routing unrelated events here
// would turn a consumer deployment mistake into a message loss incident.
func IsRoutedEvent(eventType string) bool {
	for _, candidate := range RoutedEventTypes {
		if eventType == candidate {
			return true
		}
	}
	return false
}
