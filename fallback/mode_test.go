package fallback

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeMode(t *testing.T) {
	require.Equal(t, ModeBoth, NormalizeMode(""))
	require.Equal(t, ModeReadFallback, NormalizeMode(ModeReadFallback))
}

func TestWriteEnabled(t *testing.T) {
	require.True(t, WriteEnabled(ModeBoth))
	require.True(t, WriteEnabled(ModeWriteThrough))
	require.False(t, WriteEnabled(ModeReadFallback))
	require.True(t, WriteEnabled(""))
}

func TestValidateMode(t *testing.T) {
	require.NoError(t, ValidateMode(""))
	require.NoError(t, ValidateMode(ModeBoth))
	require.Error(t, ValidateMode("invalid"))
}
