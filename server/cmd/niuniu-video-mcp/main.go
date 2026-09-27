package main

// niuniu-video-mcp — the video-creation capability module (plan §1.5).
//
// A stdio MCP server exposing five tools (quote_estimate / tts_generate /
// image_generate / video_generate / media_compose) over ONE workspace
// directory. It is deliberately self-contained: no import of server internals,
// no database, no network beyond the configured capability accounts.
//
// Wiring contract with the host:
//
//	--workspace-dir <abs>   (required) the workspace whose <ws>/video-project/
//	                        tree holds storyboard.json / assets / shots / quotes
//	                        / qc / output — every path a tool writes lives there.
//	--data-dir <abs>        (optional, default ~/.niuniu) locates the in-app
//	                        ffmpeg install (<dataDir>/bin/ffmpeg/…) and any
//	                        future caches; must match the server's cfg.DataDir.
//
// Capability accounts arrive as environment variables (frozen, §1.3):
//
//	NN_CAP_TTS_BACKEND=openai-compat   NN_CAP_TTS_BASE_URL=…  NN_CAP_TTS_API_KEY=…
//	NN_CAP_IMAGE_BACKEND=openai-compat NN_CAP_IMAGE_BASE_URL=… NN_CAP_IMAGE_API_KEY=…
//	NN_CAP_VIDEO_BACKEND=seedance|kling NN_CAP_VIDEO_BASE_URL=… NN_CAP_VIDEO_API_KEY=…
//	…plus optional extra keys (MODEL / VOICE / PRICE / PRICE_UNIT / PRICE_PER /
//	ACCESS_KEY / SECRET_KEY / TIMEOUT_SEC).
//
// An absent account is NOT fatal: that capability's tools answer with a
// specific Chinese hint (设置→能力配置), everything else keeps working. MCP
// speaks on stdout, so all logs go to stderr.

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// Version is set at build time via -ldflags -X main.Version=...
var Version = "dev"

func main() {
	wsDir := flag.String("workspace-dir", "", "workspace directory (required, absolute path)")
	dataDir := flag.String("data-dir", "", "niuniu data dir (default ~/.niuniu)")
	flag.Parse()

	if err := run(*wsDir, *dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "niuniu-video-mcp: %v\n", err)
		os.Exit(1)
	}
}

func run(wsDirFlag, dataDirFlag string) error {
	wsDir, err := resolveWorkspaceDir(wsDirFlag)
	if err != nil {
		return err
	}
	dataDir, err := resolveDataDir(dataDirFlag)
	if err != nil {
		return err
	}

	// One shared client; the adapters layer per-request timeouts on top via
	// context (NN_CAP_<CAP>_TIMEOUT_SEC), so this is only an upper bound that
	// keeps a hung provider from pinning a tool call forever.
	client := &http.Client{Timeout: 15 * time.Minute}

	reg := BuildRegistry(os.Getenv, client)
	if summary := reg.ProblemSummary(); summary != "" {
		fmt.Fprintf(os.Stderr, "niuniu-video-mcp: 能力配置问题（相关工具会明确降级）：%s\n", summary)
	}
	cfg := make([]string, 0, 3)
	if reg.TTS != nil {
		cfg = append(cfg, "tts="+reg.TTS.Name)
	}
	if reg.Image != nil {
		cfg = append(cfg, "image="+reg.Image.Name)
	}
	if reg.Video != nil {
		cfg = append(cfg, "video="+reg.Video.Name)
	}
	if len(cfg) == 0 {
		fmt.Fprintln(os.Stderr, "niuniu-video-mcp: 未配置任何付费能力（仅 quote_estimate / media_compose 可用）；在线生成类工具会提示去 设置→能力配置 添加账号。")
	} else {
		fmt.Fprintf(os.Stderr, "niuniu-video-mcp: 已配置能力：%s\n", strings.Join(cfg, ", "))
	}

	app := &App{
		wsDir:      wsDir,
		dataDir:    dataDir,
		deps:       DefaultDeps(), // → internal/ffmpegbin.Resolve(moduleDataDir())
		reg:        reg,
		httpClient: client,
	}

	s := server.NewMCPServer("niuniu-video", Version)
	registerTools(s, app)
	fmt.Fprintf(os.Stderr, "niuniu-video-mcp: workspace=%s dataDir=%s（stdio 就绪）\n", wsDir, dataDir)

	if err := server.ServeStdio(s); err != nil {
		return fmt.Errorf("MCP stdio 服务退出: %w", err)
	}
	return nil
}

// resolveWorkspaceDir validates --workspace-dir: required, absolute, existing
// directory. A wrong workspace dir is a launcher bug — fail loudly at startup
// rather than writing a video-project tree into the wrong place.
func resolveWorkspaceDir(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("缺少 --workspace-dir：请传入工作空间目录的绝对路径（工具会在其下读写 video-project/）")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("解析 --workspace-dir=%q 失败: %w", raw, err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("工作空间目录不可用（--workspace-dir=%s）：%w", abs, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("--workspace-dir=%s 不是目录", abs)
	}
	return abs, nil
}

// resolveDataDir validates --data-dir (optional; default ~/.niuniu).
func resolveDataDir(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("无法确定默认数据目录（~/.niuniu）：%w；请显式传入 --data-dir", err)
		}
		raw = filepath.Join(home, ".niuniu")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("解析 --data-dir=%q 失败: %w", raw, err)
	}
	return abs, nil
}
