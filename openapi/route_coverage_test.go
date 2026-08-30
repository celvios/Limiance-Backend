package openapi

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEveryRegisteredRouteIsDocumented(t *testing.T) {
	serverSource, err := os.ReadFile(filepath.Join("..", "internal", "platform", "httpserver", "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	v1, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := os.ReadFile("exchange-v2.yaml")
	if err != nil {
		t.Fatal(err)
	}
	routePattern := regexp.MustCompile(`mux\.Handle(?:Func)?\("(?:GET|POST|PUT|PATCH|DELETE) ([^" ]+)`)
	missing := make([]string, 0)
	for _, match := range routePattern.FindAllStringSubmatch(string(serverSource), -1) {
		route := match[1]
		var spec string
		switch {
		case strings.HasPrefix(route, "/v1"):
			route = strings.TrimPrefix(route, "/v1")
			if route == "" {
				route = "/"
			}
			spec = string(v1)
		case strings.HasPrefix(route, "/v2"):
			route = strings.TrimPrefix(route, "/v2")
			spec = string(v2)
		case route == "/ws/v2/market":
			route, spec = "/ws", string(v2)
		default:
			spec = string(v1)
		}
		if !strings.Contains(spec, "  "+route+":") {
			missing = append(missing, match[1])
		}
	}
	if len(missing) != 0 {
		t.Fatalf("registered routes missing from OpenAPI: %s", strings.Join(missing, ", "))
	}
}

func TestPrimarySpecDocumentsStandardContracts(t *testing.T) {
	content, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(content)
	for _, required := range []string{"X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Idempotency-Key", "X-API-Nonce", "HMAC-SHA256", "#/components/schemas/ApiError"} {
		if !strings.Contains(spec, required) {
			t.Fatalf("primary OpenAPI spec is missing %q", required)
		}
	}
}
