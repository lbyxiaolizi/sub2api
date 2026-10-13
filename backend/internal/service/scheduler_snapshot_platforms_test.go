//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSchedulerSnapshotPlatformsPreserveGrok(t *testing.T) {
	platforms := schedulerSnapshotPlatforms()

	require.Contains(t, platforms, PlatformGrok)
}
