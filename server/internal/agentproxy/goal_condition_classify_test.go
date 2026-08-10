package agentproxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseClassifyOutput_StructuredOutput(t *testing.T) {
	raw := []byte(`{"type":"result","structured_output":{"actionable":true,"reason":"具体任务","suggestion":"make test exits 0"}}`)
	a, err := parseClassifyOutput(raw)
	require.NoError(t, err)
	assert.True(t, a.Actionable)
	assert.Equal(t, "具体任务", a.Reason)
	assert.Equal(t, "make test exits 0", a.GoalCondition)
}

func TestParseClassifyOutput_NotActionable(t *testing.T) {
	raw := []byte(`{"type":"result","structured_output":{"actionable":false,"reason":"信息不足","suggestion":""}}`)
	a, err := parseClassifyOutput(raw)
	require.NoError(t, err)
	assert.False(t, a.Actionable)
	assert.Equal(t, "信息不足", a.Reason)
	assert.Equal(t, "", a.GoalCondition)
}

func TestParseClassifyOutput_ResultStringEnvelope(t *testing.T) {
	raw := []byte(`{"type":"result","result":"{\"actionable\":true,\"reason\":\"ok\",\"suggestion\":\"PR merged\"}"}`)
	a, err := parseClassifyOutput(raw)
	require.NoError(t, err)
	assert.True(t, a.Actionable)
	assert.Equal(t, "PR merged", a.GoalCondition)
}

func TestParseClassifyOutput_StatusFramesIgnored(t *testing.T) {
	raw := []byte(`{"status":"ready"}` + "\n" + `{"type":"result","structured_output":{"actionable":true,"reason":"r","suggestion":"s"}}`)
	a, err := parseClassifyOutput(raw)
	require.NoError(t, err)
	assert.True(t, a.Actionable)
}

func TestParseClassifyOutput_EmptyIsError(t *testing.T) {
	_, err := parseClassifyOutput([]byte(`{"status":"ready"}`))
	require.Error(t, err)
}
