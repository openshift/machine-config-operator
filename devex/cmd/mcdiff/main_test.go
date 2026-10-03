package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	rootCmd.InitDefaultCompletionCmd()
	os.Exit(m.Run())
}

func TestCompletionCommandRegistered(t *testing.T) {
	names := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		names[c.Name()] = true
	}
	assert.True(t, names["completion"], "expected cobra completion command")
	assert.True(t, names["file"])
	assert.True(t, names["diff"])
	assert.True(t, names["node"])
}

func TestCompletionBashAndZsh(t *testing.T) {
	var bash bytes.Buffer
	require.NoError(t, rootCmd.GenBashCompletion(&bash))
	assert.Contains(t, bash.String(), "mcdiff")

	var zsh bytes.Buffer
	require.NoError(t, rootCmd.GenZshCompletion(&zsh))
	assert.Contains(t, zsh.String(), "mcdiff")
}
