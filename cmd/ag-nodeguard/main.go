package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"ag-nodeguard/internal/appconfig"
	"ag-nodeguard/internal/guard"
	"ag-nodeguard/internal/installer"
)

const version = "0.5.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ag-nodeguard:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage(os.Stderr)
		return errors.New("command required")
	}
	paths, err := appconfig.ResolvePaths()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	command := args[0]
	switch command {
	case "version", "--version", "-v":
		fmt.Println("ag-nodeguard", version)
		return nil
	case "help", "--help", "-h":
		usage(os.Stdout)
		return nil
	case "install":
		if err := installer.Install(ctx, paths); err != nil {
			return err
		}
		fmt.Println("已安装并启动 ag-nodeguard；Anti Gravity 使用独立策略组。")
		return nil
	case "uninstall":
		if err := installer.Uninstall(ctx, paths); err != nil {
			return err
		}
		fmt.Println("已卸载，并移除 ag-nodeguard 的 profile 增强和运行配置。")
		return nil
	case "start":
		if err := launchStart(ctx, paths); err != nil {
			return err
		}
		fmt.Println("ag-nodeguard 已启动。")
		return nil
	case "stop":
		if err := launchStop(ctx); err != nil {
			return err
		}
		fmt.Println("ag-nodeguard 已停止；可用 start 再次加载。")
		return nil
	case "pause":
		if err := writeMarker(paths.PauseFile); err != nil {
			return err
		}
		fmt.Println("ag-nodeguard 已暂停切换和续跑；健康检查仍保留。")
		return nil
	case "resume":
		if err := os.Remove(paths.PauseFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Println("ag-nodeguard 已恢复。")
		return nil
	case "tail":
		return followLog(ctx, paths.LogFile)
	}

	cfg, err := loadConfigOrDefault(paths.ConfigFile)
	if err != nil {
		return err
	}
	logger, closeLog, err := makeLogger(paths, command == "daemon")
	if err != nil {
		return err
	}
	defer closeLog()
	engine := guard.New(cfg, paths, logger)
	switch command {
	case "daemon":
		return engine.Run(ctx)
	case "status":
		flags := flag.NewFlagSet("status", flag.ContinueOnError)
		jsonOutput := flags.Bool("json", false, "print JSON")
		watch := flags.Bool("watch", false, "stream status as newline-delimited JSON")
		interval := flags.Duration("interval", 2*time.Second, "watch refresh interval")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *watch {
			if !*jsonOutput {
				return errors.New("status --watch requires --json")
			}
			if *interval < 250*time.Millisecond || *interval > time.Hour {
				return errors.New("status --interval must be between 250ms and 1h")
			}
			return streamStatus(ctx, *interval, engine.CurrentStatus, os.Stdout)
		}
		status, err := engine.CurrentStatus(ctx)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return printJSON(status)
		}
		printStatus(status)
		return nil
	case "doctor":
		checks := engine.Doctor(ctx)
		failed := false
		for _, check := range checks {
			mark := "✓"
			if !check.OK {
				mark, failed = "✗", true
			}
			fmt.Printf("%s %-28s %s\n", mark, check.Name, check.Details)
		}
		if failed {
			return errors.New("doctor found one or more problems")
		}
		return nil
	case "candidates":
		flags := flag.NewFlagSet("candidates", flag.ContinueOnError)
		jsonOutput := flags.Bool("json", false, "print JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		candidates, err := engine.Candidates(ctx)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return printJSON(candidates)
		}
		if len(candidates) == 0 {
			fmt.Println("没有找到 <250 ms 的绿色候选节点。")
			return nil
		}
		fmt.Printf("%-5s %-12s %-9s %-10s %-7s %s\n", "序号", "套餐", "延迟", "已验证", "冷却", "节点")
		for index, candidate := range candidates {
			cooldown := "否"
			if candidate.CooldownUntil.After(time.Now()) {
				cooldown = time.Until(candidate.CooldownUntil).Round(time.Minute).String()
			}
			fmt.Printf("%-5d %-12s %-9s %-10t %-7s %s\n", index+1, candidate.SubscriptionName, fmt.Sprintf("%dms", candidate.DelayMS), candidate.KnownSuccess, cooldown, candidate.Name)
		}
		return nil
	case "subscriptions":
		flags := flag.NewFlagSet("subscriptions", flag.ContinueOnError)
		jsonOutput := flags.Bool("json", false, "print JSON")
		refresh := flags.Bool("refresh", false, "rescan subscriptions and rebuild managed configuration")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		items, err := engine.Subscriptions(ctx, *refresh)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return printJSON(items)
		}
		fmt.Printf("%-4s %-6s %-6s %-8s %s\n", "首选", "绿色", "节点", "支持", "套餐")
		for _, item := range items {
			selected := ""
			if item.Selected {
				selected = "✓"
			}
			supported := "是"
			if !item.Supported {
				supported = "否"
			}
			fmt.Printf("%-4s %-6d %-6d %-8s %s\n", selected, item.GreenNodeCount, item.NodeCount, supported, item.Name)
		}
		return nil
	case "subscription":
		if len(args) < 2 || args[1] != "select" {
			return errors.New("subscription requires: select --uid <uid> [--json]")
		}
		flags := flag.NewFlagSet("subscription select", flag.ContinueOnError)
		uid := flags.String("uid", "", "Clash Verge subscription UID")
		jsonOutput := flags.Bool("json", false, "print JSON")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if *uid == "" {
			return errors.New("subscription select requires --uid")
		}
		result, err := engine.SelectSubscription(ctx, *uid)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return printJSON(result)
		}
		fallback := ""
		if result.FallbackUsed {
			fallback = "（使用其他套餐兜底）"
		}
		fmt.Printf("首选套餐已设为 %s；当前节点 %s / %s%s。\n", result.Subscription.Name, result.Candidate.SubscriptionName, result.Candidate.Name, fallback)
		return nil
	case "once":
		flags := flag.NewFlagSet("once", flag.ContinueOnError)
		dryRun := flags.Bool("dry-run", false, "diagnose without switching")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		candidates, err := engine.DiagnoseOnce(ctx, *dryRun)
		if err != nil {
			return err
		}
		fmt.Printf("诊断完成：%d 个绿色候选", len(candidates))
		if *dryRun {
			fmt.Println("；dry-run 未切换。")
		} else if len(candidates) > 0 {
			fmt.Printf("；已受控选择 %s。\n", candidates[0].Name)
		} else {
			fmt.Println("；没有执行切换。")
		}
		return nil
	case "recover":
		if err := engine.RecoverOnce(ctx); err != nil {
			return err
		}
		fmt.Println("已执行一次后台恢复：切换绿色节点，并尝试点击当前错误任务的 Retry。")
		return nil
	default:
		usage(os.Stderr)
		return fmt.Errorf("unknown command %q", command)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `ag-nodeguard — Anti Gravity 后台节点守护程序

用法:
  ag-nodeguard install|uninstall
  ag-nodeguard start|stop|pause|resume
  ag-nodeguard status [--json] [--watch --interval 2s]
  ag-nodeguard once [--dry-run]
  ag-nodeguard recover
  ag-nodeguard doctor
  ag-nodeguard candidates [--json]
  ag-nodeguard subscriptions [--json] [--refresh]
  ag-nodeguard subscription select --uid <uid> [--json]
  ag-nodeguard tail`)
}

func loadConfigOrDefault(path string) (appconfig.Config, error) {
	cfg, err := appconfig.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return appconfig.Default(), nil
	}
	return cfg, err
}

func makeLogger(paths appconfig.Paths, persistent bool) (*log.Logger, func(), error) {
	if !persistent {
		return log.New(io.Discard, "", 0), func() {}, nil
	}
	if err := os.MkdirAll(paths.LogDir, 0o700); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(paths.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return log.New(file, "ag-nodeguard ", log.Ldate|log.Ltime|log.LUTC), func() { _ = file.Close() }, nil
}

func launchStart(ctx context.Context, paths appconfig.Paths) error {
	if _, err := os.Stat(paths.LaunchAgent); err != nil {
		return errors.New("not installed; run ag-nodeguard install first")
	}
	_ = launchStop(ctx)
	output, err := exec.CommandContext(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), paths.LaunchAgent).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func launchStop(ctx context.Context) error {
	output, err := exec.CommandContext(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d/com.local.ag-nodeguard", os.Getuid())).CombinedOutput()
	if err != nil && !strings.Contains(string(output), "Could not find service") && !strings.Contains(string(output), "No such process") {
		return fmt.Errorf("launchctl bootout: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func writeMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return file.Close()
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func streamStatus(ctx context.Context, interval time.Duration, fetch func(context.Context) (guard.Status, error), output io.Writer) error {
	encoder := json.NewEncoder(output)
	for {
		status, err := fetch(ctx)
		if err != nil {
			return err
		}
		if err := encoder.Encode(status); err != nil {
			return err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func printStatus(status guard.Status) {
	running := "已停止"
	if status.DaemonRunning {
		running = "运行中"
	}
	if status.Paused {
		running += "（已暂停切换）"
	}
	fmt.Println("守护程序:", running)
	fmt.Println("Mihomo:", empty(status.MihomoVersion, "不可用"))
	fmt.Println("当前节点:", empty(status.CurrentNode, "未知"))
	if status.CurrentDelayMS > 0 {
		fmt.Printf("延迟: %d ms\n", status.CurrentDelayMS)
	}
	fmt.Printf("Anti Gravity healthz: %t\n", status.AntigravityOK)
	if status.ExitIP != "" {
		fmt.Printf("出口: %s (%s)\n", status.ExitIP, empty(status.ExitRegion, "未知地区"))
	}
	if status.LastError != "" {
		fmt.Println("最近状态:", status.LastError)
	}
	if len(status.Cooldowns) > 0 {
		names := make([]string, 0, len(status.Cooldowns))
		for name := range status.Cooldowns {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Printf("冷却: %s 至 %s\n", name, status.Cooldowns[name].Local().Format(time.RFC3339))
		}
	}
}

func empty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func followLog(ctx context.Context, path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("log does not exist: %s", path)
	}
	command := exec.CommandContext(ctx, "/usr/bin/tail", "-n", "100", "-F", path)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	err := command.Run()
	if ctx.Err() != nil {
		return nil
	}
	return err
}
