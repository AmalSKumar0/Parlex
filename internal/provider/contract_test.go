package provider_test

import (
	"testing"
	"time"

	"parlex/internal/provider"
	"parlex/internal/provider/mock"
)

func TestMockProvider_Contract(t *testing.T) {
	mockProv := mock.New(mock.Config{EchoDelay: 5 * time.Millisecond})
	provider.RunProviderContractSuite(t, mockProv)
}
