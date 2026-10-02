package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPeopleSweepKeepsAllContextUntilTheVectorIndexIsReady(t *testing.T) {
	assert := assert.New(t)
	assert.Nil(newSweepContextJudge(nil), "no vector index means no context judgment")

	daemon := &daemonSweepSimilarity{}
	_, ok := daemon.source()
	assert.False(ok, "before vector init starts")

	handle := &vectorInitHandle{done: make(chan struct{})}
	daemon.attach(handle)
	_, ok = daemon.source()
	assert.False(ok, "while vector init is still running")

	handle.vf = &vectorFeatures{}
	_, ok = daemon.source()
	assert.False(ok, "with [vector] disabled or no hybrid engine")
}
