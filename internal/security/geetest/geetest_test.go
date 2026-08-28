package geetest

import (
	"encoding/json"
	"testing"
)

func TestParseCaptchaBodyAcceptsFrontendNamesAndSanitizesBody(t *testing.T) {
	body, captcha := parseCaptchaBody([]byte(`{"email":"user@example.com","password":"secret","lotNumber":"lot","captchaOutput":"output","passToken":"pass","genTime":"time","captchaId":"id"}`))

	if captcha.lotNumber != "lot" || captcha.captchaOutput != "output" || captcha.passToken != "pass" || captcha.genTime != "time" {
		t.Fatalf("captcha = %+v", captcha)
	}
	var sanitized map[string]any
	if err := json.Unmarshal(body, &sanitized); err != nil {
		t.Fatalf("sanitized body is invalid JSON: %v", err)
	}
	for _, field := range []string{"lotNumber", "captchaOutput", "passToken", "genTime", "captchaId"} {
		if _, ok := sanitized[field]; ok {
			t.Fatalf("sanitized body still contains %q", field)
		}
	}
	if sanitized["email"] != "user@example.com" || sanitized["password"] != "secret" {
		t.Fatalf("login fields were not preserved: %+v", sanitized)
	}
}
