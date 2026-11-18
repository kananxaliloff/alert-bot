package store_test

import (
	"testing"

	"github.com/kananxaliloff/alert-bot/internal/store"
	"github.com/kananxaliloff/alert-bot/internal/store/storetest"
)

func TestMemoryStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return store.NewMemory() })
}
