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
		_, err := fmt.Fprintln(stdout, "media-probe-worker init|batches|plan|run|credentials-template|sources-template\ninit -manifest FILE -out BATCH\nsources-template -manifest FILE -out SOURCES\nbatches -manifest FILE -batch CONFIGURED_BATCH -sources SOURCES -batch-size 10 -out BATCHES\nplan -manifest FILE -batch BATCH_OR_BATCHES [-batch-index 0] -sources SOURCES -out PLAN\nrun -manifest FILE -plan PLAN -sources SOURCES -credentials SECRET -state CHECKPOINT -out RESULTS -approve DIGEST [-reuse-state PREVIOUS] [-ffprobe ffprobe]\ncredentials-template -out SECRET\ninit/batches/plan/template 均不联网；run 只执行已审阅的有限计划，需另行授权真实 115/CDN 请求。")
		return err
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stdout)
	manifestPath := flags.String("manifest", "", "MSG 导出的原始任务清单")
	batchPath := flags.String("batch", "", "用户确认的 B 转存目录和任务批次")
	planPath := flags.String("plan", "", "离线生成的有限执行计划")
	secretPath := flags.String("credentials", "", "B 的私密令牌文件，仅 run 读取")
	sourcesPath := flags.String("sources", "", "私密来源分享配置，plan 固定摘要，run 核对")
	batchSize := flags.Int("batch-size", 10, "整库离线分批大小，最多 100")
	batchIndex := flags.Int("batch-index", -1, "从离线批次集合选择索引，从 0 开始")
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
	paths := []string{*manifestPath, *batchPath, *planPath, *secretPath, *sourcesPath, *statePath, *reusePath, *output}
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
		return service.WriteProbeWorkerJSON(*output, map[string]any{"user_id": "", "access_token": "", "refresh_token": "", "expires_at": 0, "cookie": ""})
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
	case "sources-template":
		return service.WriteProbeWorkerJSON(*output, service.ProbeWorkerSourcesTemplate(&manifest))
	case "plan", "batches":
		var batch service.ProbeWorkerBatch
		var batchSet *service.ProbeWorkerBatchSet
		if *batchIndex >= 0 {
			if args[0] != "plan" {
				return errors.New("仅 plan 支持 -batch-index")
			}
			batchSet = &service.ProbeWorkerBatchSet{}
			if err := service.ReadProbeWorkerJSON(*batchPath, batchSet); err != nil {
				return err
			}
			var err error
			batch, err = batchSet.Select(&manifest, *batchIndex)
			if err != nil {
				return err
			}
		} else {
			if err := service.ReadProbeWorkerJSON(*batchPath, &batch); err != nil {
				return err
			}
		}
		var sources *service.ProbeWorkerSources
		if *sourcesPath != "" {
			var err error
			sources, err = service.ReadProbeWorkerSources(*sourcesPath)
			if err != nil {
				return err
			}
		}
		if args[0] == "batches" {
			set, err := service.SplitProbeWorkerBatches(&manifest, batch, sources, *batchSize)
			if err != nil {
				return err
			}
			if err := service.WriteProbeWorkerJSON(*output, set); err != nil {
				return err
			}
			return json.NewEncoder(stdout).Encode(map[string]int{"batch_count": len(set.Batches)})
		}
		if batchSet != nil {
			if err := batchSet.CheckSources(sources); err != nil {
				return err
			}
		}
		plan, err := service.CreateProbeWorkerPlanWithSources(&manifest, batch, sources)
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
			StateFile: *statePath, ReuseStateFile: *reusePath, ApprovedDigest: *approved, FFprobe: *binary, ToolVersion: version, SourcesFile: *sourcesPath})
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
