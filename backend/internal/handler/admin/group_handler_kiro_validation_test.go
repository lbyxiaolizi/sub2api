package admin

import (
	"testing"

	"github.com/gin-gonic/gin/binding"
	"github.com/stretchr/testify/require"
)

func TestGroupRequestValidationRejectsUnregisteredKiroPlatform(t *testing.T) {
	createReq := CreateGroupRequest{Name: "kiro-default", Platform: "kiro"}
	require.Error(t, binding.Validator.ValidateStruct(createReq))

	updateReq := UpdateGroupRequest{Platform: "kiro"}
	require.Error(t, binding.Validator.ValidateStruct(updateReq))
}
