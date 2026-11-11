package hc

import (
	"bytes"
	"testing"
)

func TestOpenAPIIsEmbedded(t *testing.T) {
	if !bytes.Contains(openAPIYAML, []byte("openapi: 3.0.3")) || !bytes.Contains(openAPIYAML, []byte("/api/v1/spaces")) {
		t.Fatal("openapi spec was not embedded")
	}
}
