// media-probe-worker prepares finite offline plans and, only with an exact
// approved digest, probes files owned by the explicitly configured B account.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ShukeBta/MediaStationGo/internal/service"
)

var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "media-probe-worker:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		_, err := fmt.Fprintln(stdout, "media-probe-worker init|plan|run|credentials-template\ninit -manifest FILE -out BATCH\nplan -manifest FILE -batch BATCH -out PLAN\nrun -manifest FILE -plan PLAN -credentials SECRET -state CHECKPOINT -out RESULTS -approve DIGEST [-reuse-state PREVIOUS] [-ffprobe ffprobe]\ncredentials-template -out SECRET\ninit/plan/template 均不联网；run 只执行已审阅的有限计划，需另行授权真实 115/CDN 请求。")
		return err
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stdout)
	manifestPath := flags.String("manifest", "", "MSG 导出的原始任务清单")
	batchPath := flags.String("batch", "", "用户确认的 B 转存目录和任务批次")
	planPath := flags.String("plan", "", "离线生成的有限执行计划")
	secretPath := flags.String("credentials", "", "B 的私密令牌文件，仅 run 读取")
	statePath := flags.String("state", "", "持久检查点；重用时必须重新批准新计划")
	reusePath := flags.String("reuse-state", "", "只复用已完成的内容信息，不复用另一批目录")
	output := flags.String("out", "", "新建输出文件，拒绝覆盖")
	approved := flags.String("approve", "", "必须与计划 digest 完全一致")
	binary := flags.String("ffprobe", "ffprobe", "ffprobe 程序位置")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("不支持额外位置参数")
	}
	if *output == "" {
		return errors.New("必须指定 -out")
	}
	if _, err := os.Lstat(*output); !errors.Is(err, os.ErrNotExist) {
		return errors.New("输出文件已存在或不可检查，拒绝覆盖")
	}
	paths := []string{*manifestPath, *batchPath, *planPath, *secretPath, *statePath, *reusePath, *output}
	seen := map[string]bool{}
	for _, filename := range paths {
		if filename == "" {
			continue
		}
		absolute, err := filepath.Abs(filename)
		if err != nil {
			return errors.New("文件路径无效")
		}
		if seen[absolute] {
			return errors.New("输入、凭据、检查点与输出必须使用不同文件")
		}
		seen[absolute] = true
	}
	if args[0] == "credentials-template" {
		return service.WriteProbeWorkerJSON(*output, map[string]any{"user_id": "", "access_token": "", "refresh_token": "", "expires_at": 0})
	}
	var manifest service.ProbeExchangeManifest
	if err := service.ReadProbeWorkerJSON(*manifestPath, &manifest); err != nil {
		return err
	}
	if err := manifest.CheckDigest(); err != nil {
		return err
	}
	switch args[0] {
	case "init":
		return service.WriteProbeWorkerJSON(*output, service.ProbeWorkerBatchTemplate(&manifest))
	case "plan":
		var batch service.ProbeWorkerBatch
		if err := service.ReadProbeWorkerJSON(*batchPath, &batch); err != nil {
			return err
		}
		plan, err := service.CreateProbeWorkerPlan(&manifest, batch)
		if err != nil {
			return err
		}
		if err := service.WriteProbeWorkerJSON(*output, plan); err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(plan)
	case "run":
		var plan service.ProbeWorkerPlan
		if err := service.ReadProbeWorkerJSON(*planPath, &plan); err != nil {
			return err
		}
		bundle, runErr := service.RunProbeWorker(ctx, &manifest, &plan, service.ProbeWorkerRunOptions{CredentialsFile: *secretPath,
			StateFile: *statePath, ReuseStateFile: *reusePath, ApprovedDigest: *approved, FFprobe: *binary, ToolVersion: version})
		if bundle != nil {
			if err := service.WriteProbeWorkerJSON(*output, bundle); err != nil {
				return err
			}
			if err := json.NewEncoder(stdout).Encode(bundle.Stats); err != nil {
				return err
			}
		}
		return runErr
	default:
		return errors.New("未知命令，请使用 help")
	}
}
