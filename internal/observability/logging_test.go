package observability

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestJSONLoggerAddsStackToErrorsOnly(t *testing.T) {
	var output bytes.Buffer
	logger := NewJSONLogger(&output, slog.LevelInfo)
	logger.Info("ordinary")
	if strings.Contains(output.String(), `"stack"`) {
		t.Fatal("info log unexpectedly contains a stack")
	}
	output.Reset()
	logger.Error("failed")
	if !strings.Contains(output.String(), `"stack"`) {
		t.Fatalf("error log is missing stack: %s", output.String())
	}
}
