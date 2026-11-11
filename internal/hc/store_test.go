package hc

import "testing"

func TestParseRedisVersion(t *testing.T) {
	ok := "redis_version:4.0.14\r\nredis_mode:standalone\r\n"
	got, err := parseRedisVersion(ok)
	if err != nil || got != "4.0.14" {
		t.Fatalf("4.0.14: got %q err %v", got, err)
	}
	for _, sample := range []string{"redis_version:4.0.0\n", "redis_version:4.2.1\n", "redis_version:8.10.1\n"} {
		if _, err := parseRedisVersion(sample); err != nil {
			t.Fatal(sample, err)
		}
	}
	for _, sample := range []string{"redis_version:3.2.12\n", "redis_version:2.8.4\n", "no version\n"} {
		if _, err := parseRedisVersion(sample); err == nil {
			t.Fatalf("expected rejection for %q", sample)
		}
	}
}
