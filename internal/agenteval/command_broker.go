package agenteval

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	CommandBrokerSchemaVersion = 2
	maxCommandBrokerFileBytes  = 8 << 20
	commandBrokerPollInterval  = 10 * time.Millisecond
)

var (
	commandBrokerFileRE       = regexp.MustCompile(`^request-([0-9a-f]{32})\.json$`)
	commandBrokerCapabilityRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

type CommandBrokerManifest struct {
	SchemaVersion        int    `json:"schema_version"`
	RequestDirectory     string `json:"request_directory"`
	ResponseDirectory    string `json:"response_directory"`
	Capability           string `json:"capability"`
	TimeoutMillis        int64  `json:"timeout_millis"`
	AllowSyntheticWrites bool   `json:"allow_synthetic_writes,omitempty"`
	AllowReviewedWrites  bool   `json:"allow_reviewed_writes,omitempty"`
}

type CommandBrokerRequest struct {
	SchemaVersion int      `json:"schema_version"`
	ID            string   `json:"id"`
	Capability    string   `json:"capability"`
	Probe         bool     `json:"probe,omitempty"`
	Args          []string `json:"args,omitempty"`
}

type CommandBrokerResponse struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Status        string `json:"status"`
	ExitCode      int    `json:"exit_code,omitempty"`
	Stdout        []byte `json:"stdout,omitempty"`
	Stderr        []byte `json:"stderr,omitempty"`
}

type CommandBrokerConfig struct {
	RequestDirectory     string
	ResponseDirectory    string
	ManifestPath         string
	RealBinary           string
	WorkingDirectory     string
	Policy               CLICommandPolicy
	Environment          []string
	MaxStdoutBytes       int64
	MaxStderrBytes       int64
	CommandTimeout       time.Duration
	AllowSyntheticWrites bool
	AllowReviewedWrites  bool
}

type CommandBroker struct {
	config     CommandBrokerConfig
	capability string
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	closeOnce  sync.Once
	closeErr   error
	counts     map[string]int
}

func StartCommandBroker(config CommandBrokerConfig) (*CommandBroker, error) {
	if err := requireOwnerOnly("command broker working directory", config.WorkingDirectory, true); err != nil {
		return nil, err
	}
	workingDirectory, err := filepath.EvalSymlinks(config.WorkingDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve command broker working directory: %w", err)
	}
	config.WorkingDirectory = workingDirectory
	if err := validateCommandBrokerConfig(config); err != nil {
		return nil, err
	}
	capability, err := randomGatewayCapability()
	if err != nil {
		return nil, err
	}
	clientTimeout := config.CommandTimeout + 2*time.Second
	if clientTimeout > 15*time.Minute {
		clientTimeout = 15 * time.Minute
	}
	manifest := CommandBrokerManifest{
		SchemaVersion:    CommandBrokerSchemaVersion,
		RequestDirectory: config.RequestDirectory, ResponseDirectory: config.ResponseDirectory,
		Capability: capability, TimeoutMillis: clientTimeout.Milliseconds(), AllowSyntheticWrites: config.AllowSyntheticWrites,
		AllowReviewedWrites: config.AllowReviewedWrites,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	if err := writePrivateFile(config.ManifestPath, append(data, '\n')); err != nil {
		return nil, fmt.Errorf("write command broker manifest: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	broker := &CommandBroker{
		config: config, capability: capability, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), counts: map[string]int{},
	}
	go broker.serve()
	return broker, nil
}

func (b *CommandBroker) Close() error {
	b.closeOnce.Do(func() {
		b.cancel()
		<-b.done
		if err := os.Remove(b.config.ManifestPath); err != nil && !os.IsNotExist(err) {
			b.closeErr = err
		}
		for _, directory := range []string{b.config.RequestDirectory, b.config.ResponseDirectory} {
			entries, err := os.ReadDir(directory)
			if err != nil && !os.IsNotExist(err) && b.closeErr == nil {
				b.closeErr = err
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "request-") || strings.HasPrefix(entry.Name(), "processing-") || strings.HasPrefix(entry.Name(), "response-") {
					if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil && !os.IsNotExist(err) && b.closeErr == nil {
						b.closeErr = err
					}
				}
			}
		}
	})
	return b.closeErr
}

func (b *CommandBroker) serve() {
	defer close(b.done)
	ticker := time.NewTicker(commandBrokerPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			b.processAvailable()
		}
	}
}

func (b *CommandBroker) processAvailable() {
	entries, err := os.ReadDir(b.config.RequestDirectory)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && commandBrokerFileRE.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		match := commandBrokerFileRE.FindStringSubmatch(name)
		id := match[1]
		requestPath := filepath.Join(b.config.RequestDirectory, name)
		// Move the request out of the provider-writable directory before it is
		// inspected so the provider cannot replace it during validation.
		processingPath := filepath.Join(b.config.ResponseDirectory, "processing-"+id+".json")
		if err := os.Rename(requestPath, processingPath); err != nil {
			continue
		}
		response := b.processOne(processingPath, id)
		data, err := json.Marshal(response)
		if err != nil {
			continue
		}
		_ = writeCommandBrokerFileAtomic(b.config.ResponseDirectory, "response-"+id+".tmp", "response-"+id+".json", append(data, '\n'))
	}
}

func (b *CommandBroker) processOne(path, id string) CommandBrokerResponse {
	response := CommandBrokerResponse{SchemaVersion: CommandBrokerSchemaVersion, ID: id, Status: "rejected"}
	if err := requireOwnerOnly("command broker request", path, false); err != nil {
		_ = os.Remove(path)
		return response
	}
	data, err := readBoundedFile(path, maxCommandBrokerFileBytes)
	_ = os.Remove(path)
	if err != nil {
		return response
	}
	var request CommandBrokerRequest
	if err := decodeStrictJSONObject(data, &request); err != nil || request.SchemaVersion != CommandBrokerSchemaVersion || request.ID != id || !constantTimeStringEqual(request.Capability, b.capability) {
		return response
	}
	if request.Probe {
		if len(request.Args) == 0 {
			response.Status = "ready"
		}
		return response
	}
	match, err := b.config.Policy.Match(request.Args)
	if err != nil || b.counts[match.Name] >= match.MaxInvocations {
		return response
	}
	b.counts[match.Name]++
	ctx, cancel := context.WithTimeout(b.ctx, b.config.CommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, b.config.RealBinary, request.Args...)
	command.Dir = b.config.WorkingDirectory
	command.Env = append([]string(nil), b.config.Environment...)
	stdout := &boundedCommandBuffer{maximum: b.config.MaxStdoutBytes}
	stderr := &boundedCommandBuffer{maximum: b.config.MaxStderrBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	if stdout.exceeded || stderr.exceeded || ctx.Err() != nil {
		response.Status = "failed"
		return response
	}
	response.Status = "executed"
	response.Stdout = stdout.Bytes()
	response.Stderr = stderr.Bytes()
	if runErr != nil {
		var exitError *exec.ExitError
		if !errors.As(runErr, &exitError) {
			response.Status = "failed"
			response.Stdout = nil
			response.Stderr = nil
			return response
		}
		response.ExitCode = exitError.ExitCode()
	}
	return response
}

func CallCommandBroker(manifestPath string, args []string, probe bool) (CommandBrokerResponse, error) {
	manifest, err := loadCommandBrokerManifest(manifestPath)
	if err != nil {
		return CommandBrokerResponse{}, err
	}
	id, err := randomCommandBrokerID()
	if err != nil {
		return CommandBrokerResponse{}, err
	}
	request := CommandBrokerRequest{SchemaVersion: CommandBrokerSchemaVersion, ID: id, Capability: manifest.Capability, Probe: probe, Args: append([]string(nil), args...)}
	if probe {
		request.Args = nil
	}
	data, err := json.Marshal(request)
	if err != nil || len(data) > maxCommandBrokerFileBytes {
		return CommandBrokerResponse{}, fmt.Errorf("command broker request is invalid")
	}
	if err := writeCommandBrokerFileAtomic(manifest.RequestDirectory, "request-"+id+".tmp", "request-"+id+".json", append(data, '\n')); err != nil {
		return CommandBrokerResponse{}, fmt.Errorf("submit command broker request")
	}
	requestPath := filepath.Join(manifest.RequestDirectory, "request-"+id+".json")
	responsePath := filepath.Join(manifest.ResponseDirectory, "response-"+id+".json")
	defer func() {
		_ = os.Remove(requestPath)
		_ = os.Remove(responsePath)
	}()
	deadline := time.Now().Add(time.Duration(manifest.TimeoutMillis) * time.Millisecond)
	for time.Now().Before(deadline) {
		data, err := readBoundedFile(responsePath, maxCommandBrokerFileBytes)
		if os.IsNotExist(err) {
			time.Sleep(commandBrokerPollInterval)
			continue
		}
		if err != nil {
			return CommandBrokerResponse{}, fmt.Errorf("read command broker response")
		}
		if err := requireOwnerOnly("command broker response", responsePath, false); err != nil {
			return CommandBrokerResponse{}, fmt.Errorf("read command broker response")
		}
		var response CommandBrokerResponse
		if err := decodeStrictJSONObject(data, &response); err != nil || response.SchemaVersion != CommandBrokerSchemaVersion || response.ID != id || !validCommandBrokerResponse(response) {
			return CommandBrokerResponse{}, fmt.Errorf("invalid command broker response")
		}
		return response, nil
	}
	return CommandBrokerResponse{}, fmt.Errorf("command broker response timed out")
}

func validateCommandBrokerConfig(config CommandBrokerConfig) error {
	if err := requireOwnerOnly("command broker request directory", config.RequestDirectory, true); err != nil {
		return err
	}
	if err := requireOwnerOnly("command broker response directory", config.ResponseDirectory, true); err != nil {
		return err
	}
	if err := requireOwnerOnly("command broker manifest directory", filepath.Dir(config.ManifestPath), true); err != nil {
		return err
	}
	if err := requireOwnerOnly("command broker working directory", config.WorkingDirectory, true); err != nil {
		return err
	}
	if filepath.Clean(config.RequestDirectory) == filepath.Clean(config.ResponseDirectory) ||
		filepath.Dir(config.ManifestPath) == filepath.Clean(config.RequestDirectory) ||
		filepath.Dir(config.ManifestPath) == filepath.Clean(config.ResponseDirectory) ||
		config.RealBinary == "" || !filepath.IsAbs(config.RealBinary) || !filepath.IsAbs(config.WorkingDirectory) {
		return fmt.Errorf("command broker paths are invalid")
	}
	if err := config.Policy.Validate(); err != nil {
		return err
	}
	if config.MaxStdoutBytes < 1 || config.MaxStdoutBytes > 4<<20 || config.MaxStderrBytes < 1 || config.MaxStderrBytes > 1<<20 || config.CommandTimeout < time.Second || config.CommandTimeout > 15*time.Minute {
		return fmt.Errorf("command broker bounds are invalid")
	}
	return nil
}

func loadCommandBrokerManifest(path string) (CommandBrokerManifest, error) {
	if err := requireOwnerOnly("command broker manifest", path, false); err != nil {
		return CommandBrokerManifest{}, err
	}
	data, err := readBoundedFile(path, 64<<10)
	if err != nil {
		return CommandBrokerManifest{}, err
	}
	var manifest CommandBrokerManifest
	if err := decodeStrictJSONObject(data, &manifest); err != nil || manifest.SchemaVersion != CommandBrokerSchemaVersion || !commandBrokerCapabilityRE.MatchString(manifest.Capability) || manifest.TimeoutMillis < 1000 || manifest.TimeoutMillis > int64((15*time.Minute)/time.Millisecond) {
		return CommandBrokerManifest{}, fmt.Errorf("invalid command broker manifest")
	}
	if !validConfinementDirectory(manifest.RequestDirectory) || !validConfinementDirectory(manifest.ResponseDirectory) || filepath.Clean(manifest.RequestDirectory) == filepath.Clean(manifest.ResponseDirectory) {
		return CommandBrokerManifest{}, fmt.Errorf("invalid command broker manifest")
	}
	if err := requireOwnerOnly("command broker request directory", manifest.RequestDirectory, true); err != nil {
		return CommandBrokerManifest{}, err
	}
	if err := requireOwnerOnly("command broker response directory", manifest.ResponseDirectory, true); err != nil {
		return CommandBrokerManifest{}, err
	}
	return manifest, nil
}

// CommandBrokerAllowsSyntheticWrites validates the owner-only broker manifest
// before the confined proxy accepts a write-enabled synthetic invocation.
func CommandBrokerAllowsSyntheticWrites(path string) (bool, error) {
	manifest, err := loadCommandBrokerManifest(path)
	if err != nil {
		return false, err
	}
	return manifest.AllowSyntheticWrites, nil
}

// CommandBrokerAllowsReviewedWrites validates the owner-only broker manifest
// and returns the exact reviewed-write authority carried by its capability.
func CommandBrokerAllowsReviewedWrites(path string) (bool, error) {
	manifest, err := loadCommandBrokerManifest(path)
	if err != nil {
		return false, err
	}
	return manifest.AllowReviewedWrites || manifest.AllowSyntheticWrites, nil
}

func validCommandBrokerResponse(response CommandBrokerResponse) bool {
	if response.ExitCode < 0 || response.ExitCode > 255 || len(response.Stdout)+len(response.Stderr) > maxCommandBrokerFileBytes {
		return false
	}
	switch response.Status {
	case "ready":
		return response.ExitCode == 0 && len(response.Stdout) == 0 && len(response.Stderr) == 0
	case "executed":
		return true
	case "rejected", "failed":
		return response.ExitCode == 0 && len(response.Stdout) == 0 && len(response.Stderr) == 0
	default:
		return false
	}
}

func writeCommandBrokerFileAtomic(directory, temporaryName, finalName string, data []byte) error {
	temporaryPath := filepath.Join(directory, temporaryName)
	finalPath := filepath.Join(directory, finalName)
	file, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func decodeStrictJSONObject(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

func randomCommandBrokerID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func constantTimeStringEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

type boundedCommandBuffer struct {
	buffer   bytes.Buffer
	maximum  int64
	exceeded bool
}

func (b *boundedCommandBuffer) Write(data []byte) (int, error) {
	remaining := b.maximum - int64(b.buffer.Len())
	if int64(len(data)) > remaining {
		if remaining > 0 {
			_, _ = b.buffer.Write(data[:remaining])
		}
		b.exceeded = true
		return 0, io.ErrShortWrite
	}
	return b.buffer.Write(data)
}

func (b *boundedCommandBuffer) Bytes() []byte {
	return append([]byte(nil), b.buffer.Bytes()...)
}
