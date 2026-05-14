package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	MaxBytesPerResponse int         `json:"max_bytes_per_response"`
	DefaultLimit        int         `json:"default_limit"`
	Hosts               []Host      `json:"hosts"`
	Sources             []LogSource `json:"sources"`
	RedactPatterns      []string    `json:"redact_patterns"`
	compiledRedactors   []*regexp.Regexp
	hostsByID           map[string]Host
	sourcesByID         map[string]LogSource
}

type Host struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	SSHTarget  string   `json:"ssh_target"`
	SSHOptions []string `json:"ssh_options"`
}

type LogSource struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	PathGlob string `json:"path_glob"`
}

type Server struct {
	cfg Config
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func main() {
	configPath := flag.String("config", "config.json", "path to JSON config")
	listenAddr := flag.String("listen", "", "HTTP listen address; empty means stdio mode")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	s := Server{cfg: cfg}
	if *listenAddr != "" {
		if err := s.serveHTTP(*listenAddr); err != nil {
			fmt.Fprintf(os.Stderr, "serve http: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := s.serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}

func loadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.MaxBytesPerResponse <= 0 {
		cfg.MaxBytesPerResponse = 64 * 1024
	}
	if cfg.DefaultLimit <= 0 {
		cfg.DefaultLimit = 100
	}
	cfg.hostsByID = make(map[string]Host, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		if h.ID == "" {
			return Config{}, errors.New("host id is required")
		}
		if h.Type != "local" && h.Type != "ssh" {
			return Config{}, fmt.Errorf("host %q has unsupported type %q", h.ID, h.Type)
		}
		if h.Type == "ssh" && h.SSHTarget == "" {
			return Config{}, fmt.Errorf("host %q requires ssh_target", h.ID)
		}
		cfg.hostsByID[h.ID] = h
	}
	cfg.sourcesByID = make(map[string]LogSource, len(cfg.Sources))
	for _, src := range cfg.Sources {
		if src.ID == "" || src.Host == "" {
			return Config{}, errors.New("source id and host are required")
		}
		if _, ok := cfg.hostsByID[src.Host]; !ok {
			return Config{}, fmt.Errorf("source %q references unknown host %q", src.ID, src.Host)
		}
		if (src.Path == "") == (src.PathGlob == "") {
			return Config{}, fmt.Errorf("source %q must set exactly one of path or path_glob", src.ID)
		}
		cfg.sourcesByID[src.ID] = src
	}
	for _, pattern := range cfg.RedactPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return Config{}, fmt.Errorf("compile redact pattern %q: %w", pattern, err)
		}
		cfg.compiledRedactors = append(cfg.compiledRedactors, re)
	}
	return cfg, nil
}

func (s Server) serve(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(w)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		res := s.handle(req)
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s Server) serveHTTP(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		var req request
		if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024*1024)).Decode(&req); err != nil {
			writeHTTPResponse(w, response{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "parse error"},
			})
			return
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeHTTPResponse(w, s.handle(req))
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "log-mcp listening on %s\n", addr)
	return server.ListenAndServe()
}

func writeHTTPResponse(w http.ResponseWriter, res response) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (s Server) handle(req request) response {
	res := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		res.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "log-mcp",
				"version": "0.1.0",
			},
		}
	case "tools/list":
		res.Result = map[string]any{"tools": s.tools()}
	case "tools/call":
		var params callToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			res.Error = &rpcError{Code: -32602, Message: err.Error()}
			return res
		}
		result, err := s.callTool(params.Name, params.Arguments)
		if err != nil {
			res.Result = toolResult{
				IsError: true,
				Content: []toolContent{{
					Type: "text",
					Text: err.Error(),
				}},
			}
			return res
		}
		res.Result = result
	default:
		res.Error = &rpcError{Code: -32601, Message: "method not found"}
	}
	return res
}

func (s Server) tools() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_log_sources",
			"description": "List configured log sources and the hosts they belong to.",
			"inputSchema": schema("object", map[string]any{}, []string{}),
		},
		{
			"name":        "tail_log",
			"description": "Read the newest lines from one configured log source.",
			"inputSchema": schema("object", map[string]any{
				"source": map[string]any{"type": "string"},
				"lines":  map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
			}, []string{"source"}),
		},
		{
			"name":        "list_log_files",
			"description": "List concrete files matched by one configured log source.",
			"inputSchema": schema("object", map[string]any{
				"source": map[string]any{"type": "string"},
			}, []string{"source"}),
		},
		{
			"name":        "search_log",
			"description": "Search one configured log source using a regular expression.",
			"inputSchema": schema("object", map[string]any{
				"source":  map[string]any{"type": "string"},
				"pattern": map[string]any{"type": "string"},
				"context": map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
				"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
			}, []string{"source", "pattern"}),
		},
		{
			"name":        "search_all_logs",
			"description": "Search all configured log sources using a regular expression.",
			"inputSchema": schema("object", map[string]any{
				"pattern":          map[string]any{"type": "string"},
				"context":          map[string]any{"type": "integer", "minimum": 0, "maximum": 20},
				"limit_per_source": map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
			}, []string{"pattern"}),
		},
		{
			"name":        "read_log_window",
			"description": "Read lines around a line number from one concrete file under a configured source.",
			"inputSchema": schema("object", map[string]any{
				"source": map[string]any{"type": "string"},
				"file":   map[string]any{"type": "string"},
				"line":   map[string]any{"type": "integer", "minimum": 1},
				"before": map[string]any{"type": "integer", "minimum": 0, "maximum": 200},
				"after":  map[string]any{"type": "integer", "minimum": 0, "maximum": 200},
			}, []string{"source", "file", "line"}),
		},
	}
}

func schema(t string, properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":                 t,
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func (s Server) callTool(name string, args json.RawMessage) (toolResult, error) {
	switch name {
	case "list_log_sources":
		return s.textResult(s.listSources()), nil
	case "tail_log":
		var in struct {
			Source string `json:"source"`
			Lines  int    `json:"lines"`
		}
		if err := decodeArgs(args, &in); err != nil {
			return toolResult{}, err
		}
		if in.Lines <= 0 {
			in.Lines = s.cfg.DefaultLimit
		}
		out, err := s.tailLog(in.Source, clamp(in.Lines, 1, 500))
		return s.textResult(out), err
	case "list_log_files":
		var in struct {
			Source string `json:"source"`
		}
		if err := decodeArgs(args, &in); err != nil {
			return toolResult{}, err
		}
		out, err := s.listLogFiles(in.Source)
		return s.textResult(out), err
	case "search_log":
		var in struct {
			Source  string `json:"source"`
			Pattern string `json:"pattern"`
			Context int    `json:"context"`
			Limit   int    `json:"limit"`
		}
		if err := decodeArgs(args, &in); err != nil {
			return toolResult{}, err
		}
		if in.Limit <= 0 {
			in.Limit = s.cfg.DefaultLimit
		}
		out, err := s.searchLog(in.Source, in.Pattern, clamp(in.Context, 0, 20), clamp(in.Limit, 1, 500))
		return s.textResult(out), err
	case "search_all_logs":
		var in struct {
			Pattern        string `json:"pattern"`
			Context        int    `json:"context"`
			LimitPerSource int    `json:"limit_per_source"`
		}
		if err := decodeArgs(args, &in); err != nil {
			return toolResult{}, err
		}
		if in.LimitPerSource <= 0 {
			in.LimitPerSource = 50
		}
		out, err := s.searchAllLogs(in.Pattern, clamp(in.Context, 0, 20), clamp(in.LimitPerSource, 1, 200))
		return s.textResult(out), err
	case "read_log_window":
		var in struct {
			Source string `json:"source"`
			File   string `json:"file"`
			Line   int    `json:"line"`
			Before int    `json:"before"`
			After  int    `json:"after"`
		}
		if err := decodeArgs(args, &in); err != nil {
			return toolResult{}, err
		}
		out, err := s.readLogWindow(in.Source, in.File, in.Line, clamp(in.Before, 0, 200), clamp(in.After, 0, 200))
		return s.textResult(out), err
	default:
		return toolResult{}, fmt.Errorf("unknown tool %q", name)
	}
}

func decodeArgs(data json.RawMessage, v any) error {
	if len(data) == 0 {
		data = []byte("{}")
	}
	return json.Unmarshal(data, v)
}

func (s Server) textResult(text string) toolResult {
	text = s.redact(text)
	if len(text) > s.cfg.MaxBytesPerResponse {
		text = text[:s.cfg.MaxBytesPerResponse] + "\n[truncated]\n"
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: text}}}
}

func (s Server) redact(text string) string {
	for _, re := range s.cfg.compiledRedactors {
		text = re.ReplaceAllString(text, "${1}[REDACTED]")
	}
	return text
}

func (s Server) listSources() string {
	type row struct {
		ID       string `json:"id"`
		Host     string `json:"host"`
		HostType string `json:"host_type"`
		Name     string `json:"name"`
		Path     string `json:"path,omitempty"`
		PathGlob string `json:"path_glob,omitempty"`
	}
	rows := make([]row, 0, len(s.cfg.Sources))
	for _, src := range s.cfg.Sources {
		host := s.cfg.hostsByID[src.Host]
		rows = append(rows, row{
			ID:       src.ID,
			Host:     src.Host,
			HostType: host.Type,
			Name:     src.Name,
			Path:     src.Path,
			PathGlob: src.PathGlob,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	data, _ := json.MarshalIndent(rows, "", "  ")
	return string(data)
}

func (s Server) listLogFiles(sourceID string) (string, error) {
	src, host, err := s.sourceAndHost(sourceID)
	if err != nil {
		return "", err
	}
	files, err := s.resolveFiles(src, host)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "[no files]\n", nil
	}
	return strings.Join(files, "\n") + "\n", nil
}

func (s Server) tailLog(sourceID string, lines int) (string, error) {
	src, host, err := s.sourceAndHost(sourceID)
	if err != nil {
		return "", err
	}
	if host.Type == "ssh" && src.PathGlob != "" {
		return s.tailRemoteGlob(src, host, lines)
	}
	files, err := s.resolveFiles(src, host)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, file := range files {
		out, err := s.run(host, "tail", []string{"-n", strconv.Itoa(lines), file})
		writeSection(&b, src.ID, file, out, err)
	}
	return b.String(), nil
}

func (s Server) searchLog(sourceID, pattern string, contextLines, limit int) (string, error) {
	if pattern == "" {
		return "", errors.New("pattern is required")
	}
	src, host, err := s.sourceAndHost(sourceID)
	if err != nil {
		return "", err
	}
	if host.Type == "ssh" && src.PathGlob != "" {
		return s.searchRemoteGlob(src, host, pattern, contextLines, limit)
	}
	files, err := s.resolveFiles(src, host)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, file := range files {
		args := []string{"--line-number", "--no-heading", "--color", "never", "--context", strconv.Itoa(contextLines), "--max-count", strconv.Itoa(limit), pattern, file}
		out, err := s.run(host, "rg", args)
		writeSection(&b, src.ID, file, out, ignoreNoMatch(err))
	}
	return b.String(), nil
}

func (s Server) tailRemoteGlob(src LogSource, host Host, lines int) (string, error) {
	script := fmt.Sprintf(
		"for f in %s; do [ -f \"$f\" ] || continue; printf '## %s %%s\\n' \"$f\"; tail -n %d \"$f\"; printf '\\n'; done",
		src.PathGlob,
		src.ID,
		lines,
	)
	out, err := s.run(host, "sh", []string{"-lc", script})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "[no files]\n", nil
	}
	return out, nil
}

func (s Server) searchRemoteGlob(src LogSource, host Host, pattern string, contextLines, limit int) (string, error) {
	script := fmt.Sprintf(
		"rg --line-number --no-heading --color never --context %d --max-count %d -- %s %s; code=$?; [ $code -eq 0 ] || [ $code -eq 1 ]",
		contextLines,
		limit,
		shellQuote(pattern),
		src.PathGlob,
	)
	out, err := s.run(host, "sh", []string{"-lc", script})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "[no matches]\n", nil
	}
	return out, nil
}

func (s Server) searchAllLogs(pattern string, contextLines, limitPerSource int) (string, error) {
	if pattern == "" {
		return "", errors.New("pattern is required")
	}
	var b strings.Builder
	sources := append([]LogSource(nil), s.cfg.Sources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	for _, src := range sources {
		out, err := s.searchLog(src.ID, pattern, contextLines, limitPerSource)
		if err != nil {
			fmt.Fprintf(&b, "## %s\nERROR: %v\n\n", src.ID, err)
			continue
		}
		b.WriteString(out)
	}
	return b.String(), nil
}

func (s Server) readLogWindow(sourceID, file string, line, before, after int) (string, error) {
	if line <= 0 {
		return "", errors.New("line must be positive")
	}
	src, host, err := s.sourceAndHost(sourceID)
	if err != nil {
		return "", err
	}
	if err := validateFileUnderSource(src, host, file); err != nil {
		return "", err
	}
	start := line - before
	if start < 1 {
		start = 1
	}
	end := line + after
	if host.Type == "local" {
		return readLocalWindow(file, start, end)
	}
	program := fmt.Sprintf("NR>=%d && NR<=%d {print NR \":\" $0}", start, end)
	return s.run(host, "awk", []string{program, file})
}

func (s Server) sourceAndHost(sourceID string) (LogSource, Host, error) {
	src, ok := s.cfg.sourcesByID[sourceID]
	if !ok {
		return LogSource{}, Host{}, fmt.Errorf("unknown source %q", sourceID)
	}
	host := s.cfg.hostsByID[src.Host]
	return src, host, nil
}

func (s Server) resolveFiles(src LogSource, host Host) ([]string, error) {
	if src.Path != "" {
		return []string{src.Path}, nil
	}
	if host.Type == "local" {
		files, err := filepath.Glob(src.PathGlob)
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		return files, nil
	}
	out, err := s.run(host, "sh", []string{"-lc", "for f in " + src.PathGlob + "; do [ -f \"$f\" ] && printf '%s\\n' \"$f\"; done"})
	if err != nil {
		return nil, err
	}
	files := splitNonEmptyLines(out)
	sort.Strings(files)
	return files, nil
}

func (s Server) run(host Host, name string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if host.Type == "local" {
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		remote := shellJoin(append([]string{name}, args...))
		sshArgs := append([]string{}, host.SSHOptions...)
		sshArgs = append(sshArgs, host.SSHTarget, remote)
		cmd = exec.CommandContext(ctx, "ssh", sshArgs...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", errors.New("command timed out")
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return string(out), errors.New(msg)
	}
	return string(out), nil
}

func ignoreNoMatch(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "exit status 1") {
		return nil
	}
	return err
}

func writeSection(b *strings.Builder, sourceID, file, out string, err error) {
	fmt.Fprintf(b, "## %s %s\n", sourceID, file)
	if err != nil {
		fmt.Fprintf(b, "ERROR: %v\n\n", err)
		return
	}
	if strings.TrimSpace(out) == "" {
		b.WriteString("[no matches]\n\n")
		return
	}
	b.WriteString(out)
	if !strings.HasSuffix(out, "\n") {
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}

func validateFileUnderSource(src LogSource, host Host, file string) error {
	if file == "" {
		return errors.New("file is required")
	}
	if src.Path != "" {
		if file != src.Path {
			return fmt.Errorf("file %q is not allowed for source %q", file, src.ID)
		}
		return nil
	}
	if host.Type == "local" {
		matches, err := filepath.Glob(src.PathGlob)
		if err != nil {
			return err
		}
		for _, match := range matches {
			if file == match {
				return nil
			}
		}
		return fmt.Errorf("file %q is not matched by source %q", file, src.ID)
	}
	prefix := strings.TrimRight(src.PathGlob, "*")
	if !strings.HasPrefix(file, prefix) || strings.Contains(file, "..") {
		return fmt.Errorf("file %q is not allowed for source %q", file, src.ID)
	}
	return nil
}

func readLocalWindow(path string, start, end int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var b strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if line < start {
			continue
		}
		if line > end {
			break
		}
		fmt.Fprintf(&b, "%d:%s\n", line, scanner.Text())
	}
	return b.String(), scanner.Err()
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func splitNonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
