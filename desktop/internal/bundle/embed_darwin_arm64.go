//go:build darwin && arm64

package bundle

import _ "embed"

//go:embed server-bin/darwin-arm64/niuniu-server
var serverBin []byte

const serverBinName = "niuniu-server"

var serverBinKey = hashKey(serverBin)

//go:embed server-bin/darwin-arm64/niuniu-mcp
var mcpBin []byte

const mcpBinName = "niuniu-mcp"

var mcpBinKey = hashKey(mcpBin)
