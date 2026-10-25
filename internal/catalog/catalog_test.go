package catalog_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"parlex/internal/catalog"
)

func TestCatalog(t *testing.T) {
	cat, err := catalog.LoadFromFile("../../config/languages.yaml")
	require.NoError(t, err)

	langs := cat.Languages()
	assert.NotEmpty(t, langs)

	t.Run("Valid pair resolution", func(t *testing.T) {
		err := cat.ValidatePair("en", "es")
		require.NoError(t, err)

		provider, err := cat.ResolveProvider("en", "es")
		require.NoError(t, err)
		assert.Equal(t, "realtime", provider)
	})

	t.Run("Route override to pipeline for Malayalam", func(t *testing.T) {
		err := cat.ValidatePair("en", "ml")
		require.NoError(t, err)

		provider, err := cat.ResolveProvider("en", "ml")
		require.NoError(t, err)
		assert.Equal(t, "pipeline", provider)
	})

	t.Run("Rejects identical source and destination", func(t *testing.T) {
		err := cat.ValidatePair("en", "en")
		require.Error(t, err)
		assert.ErrorIs(t, err, catalog.ErrUnsupportedPair)
	})

	t.Run("Rejects unsupported language code", func(t *testing.T) {
		err := cat.ValidatePair("xyz", "en")
		require.Error(t, err)
		assert.ErrorIs(t, err, catalog.ErrUnsupportedLanguage)
	})
}
