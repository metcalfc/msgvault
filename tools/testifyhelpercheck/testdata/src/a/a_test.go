package a

import (
	"testing"

	Assert "github.com/stretchr/testify/assert"
	Require "github.com/stretchr/testify/require"
)

// Both testify styles are valid, regardless of the number of assertions.
func TestDirectAssertions(t *testing.T) {
	Require.NoError(t, nil)
	Require.NotNil(t, &struct{}{})
	Assert.Equal(t, 1, 1)
	Assert.True(t, true)
	t.Run("nested", func(t *testing.T) {
		Require.NoError(t, nil)
		Require.NotNil(t, &struct{}{})
		Assert.Equal(t, 1, 1)
		Assert.True(t, true)
	})
}

func TestBoundAssertions(t *testing.T) {
	assertions := Assert.New(t)
	requirements := Require.New(t)
	requirements.NoError(nil)
	requirements.NotNil(&struct{}{})
	assertions.Equal(1, 1)
	assertions.True(true)
}

func TestMixedAssertions(t *testing.T) {
	assertions := Assert.New(t)
	assertions.True(true)
	Require.NoError(t, nil)
	Require.NotNil(t, &struct{}{})
	Require.NoError(t, nil)
	Require.NotNil(t, &struct{}{})
}
