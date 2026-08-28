package geetest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrRejected = errors.New("captcha rejected")

type Verifier struct {
	CaptchaID  string
	PrivateKey string
	Endpoint   string
	Client     *http.Client
}

func New(captchaID, privateKey string) *Verifier {
	if strings.TrimSpace(captchaID) == "" || strings.TrimSpace(privateKey) == "" {
		return nil
	}
	return &Verifier{CaptchaID: captchaID, PrivateKey: privateKey, Endpoint: "https://gcaptcha4.geetest.com/validate", Client: &http.Client{Timeout: 5 * time.Second}}
}

func (v *Verifier) Verify(ctx context.Context, lotNumber, captchaOutput, passToken, genTime string) error {
	if v == nil || lotNumber == "" || captchaOutput == "" || passToken == "" || genTime == "" {
		return ErrRejected
	}
	signature := hmac.New(sha256.New, []byte(v.PrivateKey))
	_, _ = signature.Write([]byte(lotNumber))
	form := url.Values{
		"captcha_id":     {v.CaptchaID},
		"lot_number":     {lotNumber},
		"captcha_output": {captchaOutput},
		"pass_token":     {passToken},
		"gen_time":       {genTime},
		"sign_token":     {hex.EncodeToString(signature.Sum(nil))},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrRejected
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.Client.Do(req)
	if err != nil {
		return ErrRejected
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrRejected
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return ErrRejected
	}
	var result struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(body, &result) != nil || result.Result != "success" {
		return ErrRejected
	}
	return nil
}

func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lotNumber := r.Header.Get("X-GeeTest-Lot-Number")
		captchaOutput := r.Header.Get("X-GeeTest-Captcha-Output")
		passToken := r.Header.Get("X-GeeTest-Pass-Token")
		genTime := r.Header.Get("X-GeeTest-Gen-Time")
		if r.Body != nil && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			body, readErr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if readErr == nil {
				body, bodyCaptcha := parseCaptchaBody(body)
				if lotNumber == "" {
					lotNumber = bodyCaptcha.lotNumber
				}
				if captchaOutput == "" {
					captchaOutput = bodyCaptcha.captchaOutput
				}
				if passToken == "" {
					passToken = bodyCaptcha.passToken
				}
				if genTime == "" {
					genTime = bodyCaptcha.genTime
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
		}
		if err := v.Verify(r.Context(), lotNumber, captchaOutput, passToken, genTime); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(bytes.NewBufferString(`{"error":"captcha_required"}`).Bytes())
			return
		}
		next.ServeHTTP(w, r)
	})
}

type captchaBody struct {
	lotNumber     string
	captchaOutput string
	passToken     string
	genTime       string
}

func parseCaptchaBody(body []byte) ([]byte, captchaBody) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return body, captchaBody{}
	}
	value := func(names ...string) string {
		for _, name := range names {
			raw, ok := fields[name]
			if !ok {
				continue
			}
			var text string
			if json.Unmarshal(raw, &text) == nil {
				return text
			}
		}
		return ""
	}
	result := captchaBody{
		lotNumber:     value("lot_number", "lotNumber"),
		captchaOutput: value("captcha_output", "captchaOutput"),
		passToken:     value("pass_token", "passToken"),
		genTime:       value("gen_time", "genTime"),
	}
	for _, name := range []string{"lot_number", "lotNumber", "captcha_output", "captchaOutput", "pass_token", "passToken", "gen_time", "genTime", "captcha_id", "captchaId"} {
		delete(fields, name)
	}
	sanitized, err := json.Marshal(fields)
	if err != nil {
		return body, result
	}
	return sanitized, result
}
