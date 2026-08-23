package main

import (
	"fmt"
	"testing"
)

func TestGenericNoEndpoints404DoesNotPermanentlyBlacklist(t *testing.T) {
	class := classifyOpenRouterRetryError(fmt.Errorf("LLM stream request failed: HTTP 404: No endpoints available matching current routing"))
	if !class.Retry {
		t.Fatal("generic endpoint 404 should remain retryable")
	}
	if class.Blacklist {
		t.Fatalf("generic endpoint 404 must not permanently blacklist: %#v", class)
	}
	if class.Cooldown <= 0 {
		t.Fatalf("generic endpoint 404 should get a temporary cooldown: %#v", class)
	}
}
