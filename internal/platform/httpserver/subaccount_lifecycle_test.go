package httpserver

import (
	"net/http"
	"testing"
)

func TestSubaccountLifecycleActionFromPath(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		want   string
	}{
		{name: "freeze", method: http.MethodPost, path: "/v1/accounts/subaccounts/abc/freeze", want: "freeze"},
		{name: "unfreeze", method: http.MethodPost, path: "/v1/accounts/subaccounts/abc/unfreeze", want: "unfreeze"},
		{name: "delete", method: http.MethodDelete, path: "/v1/accounts/subaccounts/abc", want: "deleted"},
		{name: "unknown", method: http.MethodPost, path: "/v1/accounts/subaccounts/abc/other", want: ""},
	}

	for _, tc := range cases {
		req, err := http.NewRequest(tc.method, tc.path, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := subaccountLifecycleAction(req)
		if got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
