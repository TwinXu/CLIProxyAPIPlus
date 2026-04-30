package main

import (
	"encoding/hex"
	"fmt"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/cursor/proto"
)

func main() {
	resultBytes := cursorproto.EncodeExecMcpResult(1, "exec-id", `{"test":"data"}`, false)
	fmt.Printf("Exec MCP result protobuf hex: %s\n", hex.EncodeToString(resultBytes))
	fmt.Printf("Result length: %d bytes\n", len(resultBytes))
}
