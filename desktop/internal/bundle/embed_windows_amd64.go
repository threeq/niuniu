//go:build windows && amd64

package bundle

import _ "embed"

//go:embed server-bin/windows-amd64/niuniu-server.exe
var serverBin []byte

const serverBinName = "niuniu-server.exe"

var serverBinKey = hashKey(serverBin)

//go:embed server-bin/windows-amd64/niuniu-mcp.exe
var mcpBin []byte

const mcpBinName = "niuniu-mcp.exe"

var mcpBinKey = hashKey(mcpBin)
