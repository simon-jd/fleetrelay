package main

import (
	"math"
	"testing"
	"time"
)

func TestPublicationBackoffBounds(t *testing.T) {
	for _, tc := range []struct {
		failures  int
		low, high time.Duration
	}{
		{-1, 500 * time.Millisecond, time.Second}, {0, 500 * time.Millisecond, time.Second},
		{1, time.Second, 2 * time.Second}, {4, 8 * time.Second, 16 * time.Second},
		{5, 15 * time.Second, 30 * time.Second}, {math.MaxInt32, 15 * time.Second, 30 * time.Second},
	} {
		for range 100 {
			if got := publicationBackoff(tc.failures); got < tc.low || got > tc.high {
				t.Fatalf("failure count %d: %s outside [%s,%s]", tc.failures, got, tc.low, tc.high)
			}
		}
	}
}
