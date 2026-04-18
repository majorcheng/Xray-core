package reality

import (
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

const (
	randRangeMinValue = int64(3)
	randRangeMaxValue = int64(7)
	randRangeAttempts = 128
)

func clearMldsaKeyCache() {
	mldsaKeyCache.Range(func(key, value any) bool {
		mldsaKeyCache.Delete(key)
		return true
	})
}

func TestGetCachedMldsaPublicKeyCachesParsedKey(t *testing.T) {
	clearMldsaKeyCache()

	pubKey, _, err := mldsa65.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	raw, err := pubKey.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary() error = %v", err)
	}

	first := getCachedMldsaPublicKey(raw)
	second := getCachedMldsaPublicKey(raw)
	if first == nil || second == nil {
		t.Fatal("getCachedMldsaPublicKey() returned nil")
	}
	if first != second {
		t.Fatal("getCachedMldsaPublicKey() did not reuse cached key")
	}
}

func TestGetCachedMldsaPublicKeyRejectsInvalidData(t *testing.T) {
	clearMldsaKeyCache()

	if got := getCachedMldsaPublicKey([]byte("invalid")); got != nil {
		t.Fatal("getCachedMldsaPublicKey() should return nil for invalid data")
	}
}

func TestGetPathLockedEmptyMapReturnsRoot(t *testing.T) {
	if got := getPathLocked(map[string]struct{}{}); got != "/" {
		t.Fatalf("getPathLocked() = %q, want %q", got, "/")
	}
}

func TestRandRangeRespectsBounds(t *testing.T) {
	if got := randRange(randRangeMaxValue, randRangeMinValue); got != randRangeMaxValue {
		t.Fatalf("randRange(max<=min) = %d, want %d", got, randRangeMaxValue)
	}

	for range randRangeAttempts {
		got := randRange(randRangeMinValue, randRangeMaxValue)
		if got < randRangeMinValue || got >= randRangeMaxValue {
			t.Fatalf("randRange() = %d, want [%d, %d)", got, randRangeMinValue, randRangeMaxValue)
		}
	}
}
