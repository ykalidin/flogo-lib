package engine

import (
	"github.com/stretchr/testify/assert"
	"os"
	"strconv"
	"testing"
)

//TestNewPooledConfigOk
func TestNewPooledConfigDefault(t *testing.T) {
	pooledConfig := NewPooledConfig()

	// assert Success
	assert.Equal(t, RUNNER_WORKERS_DEFAULT, pooledConfig.NumWorkers)
	assert.Equal(t, RUNNER_QUEUE_SIZE_DEFAULT, pooledConfig.WorkQueueSize)
}

//TestNewPooledConfigOk
func TestNewPooledConfigOverride(t *testing.T) {
	previousWorkers := os.Getenv(RUNNER_WORKERS_KEY)
	defer os.Setenv(RUNNER_WORKERS_KEY, previousWorkers)
	previousQueue := os.Getenv(RUNNER_QUEUE_SIZE_KEY)
	defer os.Setenv(RUNNER_QUEUE_SIZE_KEY, previousQueue)

	newWorkersValue := 6
	newQueueValue := 60

	// Change values
	os.Setenv(RUNNER_WORKERS_KEY, strconv.Itoa(newWorkersValue))
	os.Setenv(RUNNER_QUEUE_SIZE_KEY, strconv.Itoa(newQueueValue))

	pooledConfig := NewPooledConfig()

	// assert Success
	assert.Equal(t, newWorkersValue, pooledConfig.NumWorkers)
	assert.Equal(t, newQueueValue, pooledConfig.WorkQueueSize)
}
