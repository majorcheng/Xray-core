package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	logService "github.com/xtls/xray-core/app/log/command"
	routerService "github.com/xtls/xray-core/app/router/command"
	"github.com/xtls/xray-core/common/cmdarg"
	"github.com/xtls/xray-core/common/platform"
	"github.com/xtls/xray-core/core"
	iconf "github.com/xtls/xray-core/infra/conf"
	confserial "github.com/xtls/xray-core/infra/conf/serial"
	"github.com/xtls/xray-core/main/commands/base"
	"github.com/xtls/xray-core/main/confloader"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

var cmdReload = &base.Command{
	CustomFlags: true,
	UsageLine:   "{{.Exec}} reload [all|routing,log,dns,observatory] [-s 127.0.0.1:10085] [-c config.json] [-confdir dir]",
	Short:       "Reload config in running Xray process via API",
	Long: `Reload applies selected modules to a running Xray instance via API.

Examples:
  {{.Exec}} reload
  {{.Exec}} reload all
  {{.Exec}} reload routing,log
  {{.Exec}} reload -s 127.0.0.1:10085 routing -confdir /etc/xray/conf.d

Notes:
  - No argument means all supported modules.
  - Unsupported modules are skipped.
`,
	Run: executeReload,
}

var (
	reloadServerAddr string
	reloadTimeout    int
	reloadFormat     string
	reloadConfigDir  string
	reloadConfigFile cmdarg.Arg
)

func init() {
	cmdReload.Flag.StringVar(&reloadServerAddr, "s", "", "API server address")
	cmdReload.Flag.StringVar(&reloadServerAddr, "server", "", "API server address")
	cmdReload.Flag.IntVar(&reloadTimeout, "t", 5, "API timeout in seconds")
	cmdReload.Flag.IntVar(&reloadTimeout, "timeout", 5, "API timeout in seconds")
	cmdReload.Flag.StringVar(&reloadFormat, "format", "auto", "Format of input file")
	cmdReload.Flag.StringVar(&reloadConfigDir, "confdir", "", "A dir with multiple config files")
	cmdReload.Flag.Var(&reloadConfigFile, "config", "Config path for reload")
	cmdReload.Flag.Var(&reloadConfigFile, "c", "Short alias of -config")
}

var supportedReloadScopes = map[string]struct{}{
	"all":         {},
	"routing":     {},
	"log":         {},
	"dns":         {},
	"observatory": {},
}

func executeReload(cmd *base.Command, args []string) {
	cmd.Flag.Parse(args)
	scopes, err := parseReloadScopes(cmd.Flag.Args())
	if err != nil {
		base.Fatalf("invalid reload scope: %s (supported: %s)", err, listSupportedModules())
	}

	needConfig := strings.TrimSpace(reloadServerAddr) == ""

	needConfigFiles := needConfig || scopes["all"] || scopes["routing"]
	var configFiles cmdarg.Arg
	if needConfigFiles {
		configFiles, err = getReloadConfigFilePath(true)
		if err != nil {
			base.Fatalf("failed to resolve config files for reload: %s", err)
		}
	}

	var rawConfig *iconf.Config
	if needConfig {
		rawConfig, err = loadReloadRawConfigFromFiles(scopes, strings.TrimSpace(reloadServerAddr) == "", configFiles)
		if err != nil {
			base.Fatalf("failed to load config for reload: %s", err)
		}
	}

	var routingConfigFiles []string
	if scopes["all"] || scopes["routing"] {
		routingConfigFiles, err = buildRoutingReloadConfigFiles(configFiles)
		if err != nil {
			base.Fatalf("failed to prepare routing config files for reload: %s", err)
		}
	}

	apiServerAddr := strings.TrimSpace(reloadServerAddr)
	if apiServerAddr == "" {
		resolved, rerr := resolveAPIServerAddrFromRawConfig(rawConfig)
		if rerr == nil {
			apiServerAddr = resolved
			fmt.Printf("Using API server from config: %s\n", apiServerAddr)
		} else {
			apiServerAddr = "127.0.0.1:10085"
			fmt.Printf("API server not found in config, fallback to default: %s\n", apiServerAddr)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(reloadTimeout)*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, apiServerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		base.Fatalf("failed to dial api server %s: %s", apiServerAddr, err)
	}
	defer conn.Close()

	results := make([]string, 0, 5)
	if scopes["all"] || scopes["routing"] {
		results = append(results, reloadRoutingByAPI(ctx, conn, routingConfigFiles, normalizeReloadConfigFormat()))
	}
	if scopes["all"] || scopes["log"] {
		results = append(results, reloadLogByAPI(ctx, conn))
	}
	if scopes["all"] || scopes["dns"] {
		results = append(results, "dns: skipped (no DNS API reload service)")
	}
	if scopes["all"] || scopes["observatory"] {
		results = append(results, "observatory: skipped (no Observatory API reload service)")
	}

	for _, line := range results {
		fmt.Println(line)
	}
}

func parseReloadScopes(args []string) (map[string]bool, error) {
	if len(args) == 0 {
		return map[string]bool{"all": true}, nil
	}
	if len(args) > 1 {
		return nil, fmt.Errorf("unexpected extra arguments: %v", args[1:])
	}

	raw := strings.TrimSpace(args[0])
	if raw == "" {
		return map[string]bool{"all": true}, nil
	}

	out := map[string]bool{}
	for _, token := range strings.Split(raw, ",") {
		scope := strings.ToLower(strings.TrimSpace(token))
		if scope == "" {
			continue
		}
		if _, ok := supportedReloadScopes[scope]; !ok {
			return nil, fmt.Errorf("unknown module %q", scope)
		}
		out[scope] = true
	}

	if len(out) == 0 {
		return map[string]bool{"all": true}, nil
	}
	if out["all"] && len(out) > 1 {
		return nil, fmt.Errorf("'all' cannot be combined with other modules")
	}
	return out, nil
}

func loadReloadRawConfigFromFiles(scopes map[string]bool, needAPI bool, files cmdarg.Arg) (*iconf.Config, error) {
	format := normalizeReloadConfigFormat()
	sources, err := buildReloadConfigSources(format, files)
	if err != nil {
		return nil, err
	}
	return mergeReloadConfigForScopes(sources, scopes, needAPI)
}

func buildReloadConfigSources(formatName string, files cmdarg.Arg) ([]*core.ConfigSource, error) {
	sources := make([]*core.ConfigSource, len(files))
	for i, file := range files {
		f := formatName
		if f == "auto" {
			if file != "stdin:" {
				f = core.GetFormat(file)
			} else {
				f = "json"
			}
		}
		if f == "" {
			return nil, fmt.Errorf("failed to get format of %s", file)
		}
		if f == "protobuf" {
			return nil, fmt.Errorf("protobuf config is not supported in selective reload")
		}
		sources[i] = &core.ConfigSource{Name: file, Format: f}
	}
	return sources, nil
}

func mergeReloadConfigForScopes(files []*core.ConfigSource, scopes map[string]bool, needAPI bool) (*iconf.Config, error) {
	merged := &iconf.Config{}
	for _, file := range files {
		errorsLogInfo("Reading config: ", file)
		r, err := confloader.LoadConfig(file.Name)
		if err != nil {
			return nil, fmt.Errorf("failed to read config: %v: %w", file, err)
		}
		decoder := confserial.ReaderDecoderByFormat[file.Format]
		if decoder == nil {
			return nil, fmt.Errorf("unsupported config format: %s", file.Format)
		}
		parsed, err := decoder(r)
		if err != nil {
			return nil, fmt.Errorf("failed to decode config: %v: %w", file, err)
		}
		mergeParsedConfigByScope(merged, parsed, file.Name, scopes, needAPI)
	}
	return merged, nil
}

func mergeParsedConfigByScope(dst, src *iconf.Config, _ string, scopes map[string]bool, needAPI bool) {
	if (scopes["all"] || scopes["routing"]) && src.RouterConfig != nil {
		dst.RouterConfig = src.RouterConfig
	}
	if needAPI && src.API != nil {
		dst.API = src.API
	}
	if (scopes["all"] || scopes["log"]) && src.LogConfig != nil {
		dst.LogConfig = src.LogConfig
	}
	if !needAPI || len(src.InboundConfigs) == 0 {
		return
	}
	for i := range src.InboundConfigs {
		idx := -1
		for j := range dst.InboundConfigs {
			if dst.InboundConfigs[j].Tag == src.InboundConfigs[i].Tag {
				idx = j
				break
			}
		}
		if idx > -1 {
			dst.InboundConfigs[idx] = src.InboundConfigs[i]
		} else {
			dst.InboundConfigs = append(dst.InboundConfigs, src.InboundConfigs[i])
		}
	}
}

func errorsLogInfo(msg ...interface{}) {
	log.Println(msg...)
}

func getReloadConfigFilePath(verbose bool) (cmdarg.Arg, error) {
	configFiles := append(cmdarg.Arg(nil), reloadConfigFile...)
	if dirExists(reloadConfigDir) {
		if verbose {
			log.Println("Using confdir from arg:", reloadConfigDir)
		}
		configFiles = readReloadConfDir(configFiles, reloadConfigDir)
	}

	if len(configFiles) > 0 {
		return configFiles, nil
	}

	if discoveredFiles, discoveredConfDir, err := discoverReloadConfigFromRunningProcess(); err == nil {
		if len(discoveredFiles) > 0 {
			if verbose {
				log.Println("Using config from running xray process args:", discoveredFiles.String())
			}
			return discoveredFiles, nil
		}
		if discoveredConfDir != "" {
			if verbose {
				log.Println("Using confdir from running xray process args:", discoveredConfDir)
			}
			return readReloadConfDir(nil, discoveredConfDir), nil
		}
	}

	if envConfDir := platform.GetConfDirPath(); dirExists(envConfDir) {
		if verbose {
			log.Println("Using confdir from env:", envConfDir)
		}
		return readReloadConfDir(nil, envConfDir), nil
	}

	suffixes := []string{".json", ".jsonc", ".toml", ".yaml", ".yml"}
	if workingDir, err := os.Getwd(); err == nil {
		for _, suffix := range suffixes {
			configFile := filepath.Join(workingDir, "config"+suffix)
			if fileExists(configFile) {
				if verbose {
					log.Println("Using default config:", configFile)
				}
				return cmdarg.Arg{configFile}, nil
			}
		}
	}

	if configFile := platform.GetConfigurationPath(); fileExists(configFile) {
		if verbose {
			log.Println("Using config from env:", configFile)
		}
		return cmdarg.Arg{configFile}, nil
	}

	return nil, fmt.Errorf("no config file found for reload; please specify -c or -confdir")
}

func discoverReloadConfigFromRunningProcess() (cmdarg.Arg, string, error) {
	if runtime.GOOS != "linux" {
		return nil, "", fmt.Errorf("process args discovery is only supported on linux")
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, "", err
	}

	type configHint struct {
		files   cmdarg.Arg
		confdir string
		pid     int
	}
	var hints []configHint

	self := os.Getpid()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		cmdlineBytes, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || len(cmdlineBytes) == 0 {
			continue
		}
		args := splitCmdline(cmdlineBytes)
		if len(args) < 2 || args[1] != "run" {
			continue
		}
		execName := strings.ToLower(filepath.Base(args[0]))
		if !strings.Contains(execName, "xray") {
			continue
		}

		files, confdir := parseRunConfigHints(args[2:])
		if len(files) == 0 && confdir == "" {
			continue
		}
		hints = append(hints, configHint{files: files, confdir: confdir, pid: pid})
	}

	if len(hints) == 0 {
		return nil, "", fmt.Errorf("no running xray process with config hints found")
	}
	if len(hints) > 1 {
		pids := make([]int, 0, len(hints))
		for _, h := range hints {
			pids = append(pids, h.pid)
		}
		return nil, "", fmt.Errorf("multiple running xray processes with config hints found: %v", pids)
	}

	return hints[0].files, hints[0].confdir, nil
}

func parseRunConfigHints(args []string) (cmdarg.Arg, string) {
	var files cmdarg.Arg
	var confdir string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "-config":
			if i+1 < len(args) {
				files = append(files, args[i+1])
				i++
			}
		case strings.HasPrefix(a, "-c="):
			files = append(files, strings.TrimPrefix(a, "-c="))
		case strings.HasPrefix(a, "-config="):
			files = append(files, strings.TrimPrefix(a, "-config="))
		case a == "-confdir":
			if i+1 < len(args) {
				confdir = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "-confdir="):
			confdir = strings.TrimPrefix(a, "-confdir=")
		}
	}
	return files, confdir
}

func splitCmdline(cmdline []byte) []string {
	raw := strings.Split(string(cmdline), "\x00")
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func readReloadConfDir(configFiles cmdarg.Arg, dirPath string) cmdarg.Arg {
	confs, err := os.ReadDir(dirPath)
	if err != nil {
		log.Fatalln(err)
	}
	pattern := getReloadRegexpByFormat()
	for _, f := range confs {
		matched, err := regexp.MatchString(pattern, f.Name())
		if err != nil {
			log.Fatalln(err)
		}
		if matched {
			configFiles = append(configFiles, path.Join(dirPath, f.Name()))
		}
	}
	return configFiles
}

func getReloadRegexpByFormat() string {
	switch normalizeReloadConfigFormat() {
	case "json":
		return `^.+\.(json|jsonc)$`
	case "toml":
		return `^.+\.toml$`
	case "yaml", "yml":
		return `^.+\.(yaml|yml)$`
	default:
		return `^.+\.(json|jsonc|toml|yaml|yml)$`
	}
}

func resolveAPIServerAddrFromRawConfig(cfg *iconf.Config) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config is nil")
	}

	if cfg.API == nil {
		return "", fmt.Errorf("api app not found")
	}
	if listen := strings.TrimSpace(cfg.API.Listen); listen != "" {
		if strings.Contains(listen, ":") {
			return listen, nil
		}
		if p, err := strconv.Atoi(listen); err == nil && p > 0 {
			return net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), nil
		}
		return "", fmt.Errorf("invalid api.listen: %s", listen)
	}

	apiTag := strings.TrimSpace(cfg.API.Tag)
	if apiTag == "" {
		apiTag = "api"
	}

	for _, in := range cfg.InboundConfigs {
		if in.Tag != apiTag {
			continue
		}
		if in.PortList == nil || len(in.PortList.Range) == 0 {
			return "", fmt.Errorf("api inbound has no port")
		}
		port := int(in.PortList.Range[0].From)
		if port <= 0 {
			return "", fmt.Errorf("api inbound has invalid port")
		}

		host := "127.0.0.1"
		if in.ListenOn != nil && in.ListenOn.Address != nil {
			host = in.ListenOn.Address.String()
		}
		if host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		return net.JoinHostPort(host, strconv.Itoa(port)), nil
	}

	return "", fmt.Errorf("cannot resolve api inbound by tag %q", apiTag)
}

func reloadRoutingByAPI(ctx context.Context, conn *grpc.ClientConn, configFiles []string, format string) string {
	if len(configFiles) == 0 {
		return "routing: skipped (config unavailable)"
	}
	client := routerService.NewRoutingServiceClient(conn)
	_, err := client.ReloadRoutingConfig(ctx, &routerService.ReloadRoutingConfigRequest{
		Format:      format,
		ConfigFiles: configFiles,
	})
	if err != nil {
		return formatAPIResult("routing", err)
	}
	return "routing: ok"
}

func buildRoutingReloadConfigFiles(files cmdarg.Arg) ([]string, error) {
	configFiles := make([]string, 0, len(files))
	for _, file := range files {
		file = strings.TrimSpace(file)
		switch {
		case file == "":
			continue
		case file == "stdin:":
			return nil, fmt.Errorf("routing reload via API does not support stdin")
		case strings.Contains(file, "://"):
			return nil, fmt.Errorf("routing reload via API does not support remote config URLs: %s", file)
		default:
			configFiles = append(configFiles, file)
		}
	}
	if len(configFiles) == 0 {
		return nil, fmt.Errorf("no local config files available for routing reload")
	}
	return configFiles, nil
}

func normalizeReloadConfigFormat() string {
	format := core.GetFormatByExtension(reloadFormat)
	if format == "" {
		return "auto"
	}
	return format
}

func reloadLogByAPI(ctx context.Context, conn *grpc.ClientConn) string {
	client := logService.NewLoggerServiceClient(conn)
	_, err := client.RestartLogger(ctx, &logService.RestartLoggerRequest{})
	if err != nil {
		return formatAPIResult("log", err)
	}
	return "log: ok"
}

func formatAPIResult(module string, err error) string {
	st, ok := status.FromError(err)
	if ok {
		if st.Code() == codes.Unimplemented {
			return fmt.Sprintf("%s: skipped (service not enabled)", module)
		}
		return fmt.Sprintf("%s: failed (%s)", module, st.Message())
	}
	return fmt.Sprintf("%s: failed (%v)", module, err)
}

func listSupportedModules() string {
	mods := make([]string, 0, len(supportedReloadScopes))
	for mod := range supportedReloadScopes {
		mods = append(mods, mod)
	}
	sort.Strings(mods)
	return strings.Join(mods, ",")
}
