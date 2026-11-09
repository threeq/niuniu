package service

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildMultipartBodyWritesFieldsAndFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cover.png")
	require.NoError(t, os.WriteFile(path, []byte("image-bytes"), 0o600))

	encoded, contentType, err := buildMultipartBody(map[string]any{"type": "thumb"}, map[string]string{"media": path}, root)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	mediaType, params, err := mime.ParseMediaType(contentType)
	require.NoError(t, err)
	require.Equal(t, "multipart/form-data", mediaType)

	reader := multipart.NewReader(bytes.NewReader(encoded), params["boundary"])
	seen := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		data, err := io.ReadAll(part)
		require.NoError(t, err)
		seen[part.FormName()] = string(data)
	}
	require.Equal(t, "thumb", seen["type"])
	require.Equal(t, "image-bytes", seen["media"])
}

func TestBuildMultipartBodyRejectsFileOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("nope"), 0o600))

	_, _, err := buildMultipartBody(nil, map[string]string{"media": outside}, root)
	require.Error(t, err)

	_, err = confinedFile(outside, root)
	require.Error(t, err)
}

func TestBuildMultipartBodyNeedsAFile(t *testing.T) {
	_, _, err := buildMultipartBody(map[string]any{"type": "thumb"}, nil, t.TempDir())
	require.Error(t, err)
}
