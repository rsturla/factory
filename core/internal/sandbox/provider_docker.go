package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// DockerProvider uses local Docker for sandbox execution (local dev only).
// No security isolation - functional testing only.
type DockerProvider struct {
	cli *client.Client
}

// NewDockerProvider creates a Docker-based sandbox provider.
func NewDockerProvider() (*DockerProvider, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &DockerProvider{cli: cli}, nil
}

// Create provisions a Docker container.
func (d *DockerProvider) Create(ctx context.Context, spec SandboxSpec) (*SandboxHandle, error) {
	// Convert environment map to []string
	env := make([]string, 0, len(spec.Environment))
	for k, v := range spec.Environment {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Parse resource limits
	hostConfig := &container.HostConfig{
		AutoRemove: false,
	}

	// Apply memory limit if specified (convert K8s-style to bytes)
	// For Phase 1: basic support. Full K8s quantity parsing in later phases.
	if spec.Resources.MemoryLimit != "" {
		// Simple parsing: "512Mi" -> 512 * 1024 * 1024
		// Full implementation would use k8s.io/apimachinery/pkg/api/resource
		// For now, just set a default if non-empty
		hostConfig.Resources.Memory = 512 * 1024 * 1024 // 512Mi default
	}

	// Create container
	resp, err := d.cli.ContainerCreate(ctx,
		&container.Config{
			Image: spec.Image,
			Env:   env,
			Tty:   false,
			Cmd:   []string{"sleep", "3600"}, // keep container alive
		},
		hostConfig,
		nil, nil, spec.ID)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	// Start container
	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}

	return &SandboxHandle{
		ID:     spec.ID,
		Status: SandboxPhaseProvisioning,
	}, nil
}

// Get retrieves container status.
func (d *DockerProvider) Get(ctx context.Context, id string) (*SandboxStatus, error) {
	inspect, err := d.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("inspect container: %w", err)
	}

	phase := SandboxPhaseProvisioning
	ready := false
	if inspect.State.Running {
		phase = SandboxPhaseReady
		ready = true
	} else if inspect.State.Error != "" {
		phase = SandboxPhaseError
	}

	createdAt, _ := time.Parse(time.RFC3339Nano, inspect.Created)

	return &SandboxStatus{
		ID:         id,
		Phase:      phase,
		Ready:      ready,
		Message:    inspect.State.Status,
		InstanceID: inspect.ID,
		CreatedAt:  createdAt,
	}, nil
}

// Exec runs a command in the container.
func (d *DockerProvider) Exec(ctx context.Context, id string, cmd []string, stdin []byte) (*ExecResult, error) {
	execResp, err := d.cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  len(stdin) > 0,
	})
	if err != nil {
		return nil, fmt.Errorf("create exec: %w", err)
	}

	attach, err := d.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecStartOptions{})
	if err != nil {
		return nil, fmt.Errorf("attach exec: %w", err)
	}
	defer attach.Close()

	// Write stdin if provided
	if len(stdin) > 0 {
		if _, err := attach.Conn.Write(stdin); err != nil {
			return nil, fmt.Errorf("write stdin: %w", err)
		}
		attach.CloseWrite()
	}

	// Read output (demultiplex stdout/stderr)
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attach.Reader); err != nil {
		return nil, fmt.Errorf("read output: %w", err)
	}

	// Get exit code
	inspectResp, err := d.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return nil, fmt.Errorf("inspect exec: %w", err)
	}

	return &ExecResult{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: inspectResp.ExitCode,
	}, nil
}

// ExecDetached starts a command in background.
func (d *DockerProvider) ExecDetached(ctx context.Context, id string, cmd []string) (string, error) {
	execResp, err := d.cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:    cmd,
		Detach: true,
	})
	if err != nil {
		return "", fmt.Errorf("create detached exec: %w", err)
	}

	// Start but don't attach (runs in background)
	if err := d.cli.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{Detach: true}); err != nil {
		return "", fmt.Errorf("start detached exec: %w", err)
	}

	return execResp.ID, nil
}

// ExecStatus checks detached process status.
func (d *DockerProvider) ExecStatus(ctx context.Context, id string, execID string) (*ExecStatusResult, error) {
	inspectResp, err := d.cli.ContainerExecInspect(ctx, execID)
	if err != nil {
		return nil, fmt.Errorf("inspect exec: %w", err)
	}

	return &ExecStatusResult{
		Running:  inspectResp.Running,
		ExitCode: inspectResp.ExitCode,
	}, nil
}

// CopyFrom extracts a file from the container.
func (d *DockerProvider) CopyFrom(ctx context.Context, id string, path string) ([]byte, error) {
	reader, _, err := d.cli.CopyFromContainer(ctx, id, path)
	if err != nil {
		return nil, fmt.Errorf("copy from container: %w", err)
	}
	defer reader.Close()

	// Read tar archive
	tr := tar.NewReader(reader)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar: %w", err)
		}

		if header.Typeflag == tar.TypeReg {
			// Enforce size limit (100MB max)
			const maxOutputSize = 100 * 1024 * 1024
			if header.Size > maxOutputSize {
				return nil, fmt.Errorf("file too large: %d bytes (max %d)", header.Size, maxOutputSize)
			}

			data, err := io.ReadAll(io.LimitReader(tr, maxOutputSize))
			if err != nil {
				return nil, fmt.Errorf("read file from tar: %w", err)
			}
			return data, nil
		}
	}

	return nil, fmt.Errorf("file not found in archive: %s", path)
}

// CopyTo writes a file to the container.
func (d *DockerProvider) CopyTo(ctx context.Context, id string, path string, data []byte) error {
	// Create tar archive in memory
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Extract directory and basename
	dir := filepath.Dir(path)
	basename := filepath.Base(path)

	header := &tar.Header{
		Name: basename,
		Mode: 0644,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("write tar data: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}

	// Upload to container directory
	if err := d.cli.CopyToContainer(ctx, id, dir, &buf, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copy to container: %w", err)
	}

	return nil
}

// Delete stops and removes the container.
func (d *DockerProvider) Delete(ctx context.Context, id string) error {
	// Add timeout to prevent hanging on stuck containers
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	timeout := 10
	if err := d.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil {
		// Ignore error if already stopped
	}

	if err := d.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("remove container: %w", err)
	}

	return nil
}

// CopyDirToContainer copies a directory from host to container.
func (d *DockerProvider) CopyDirToContainer(ctx context.Context, containerID, srcPath, dstPath string) error {
	// Create tar archive of source directory
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Walk directory tree
	err := filepath.Walk(srcPath, func(file string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Create tar header
		header, err := tar.FileInfoHeader(fi, file)
		if err != nil {
			return err
		}

		// Update header name to be relative to srcPath
		relPath, err := filepath.Rel(srcPath, file)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relPath)

		// Write header
		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		// Write file content if regular file
		if fi.Mode().IsRegular() {
			data, err := os.Open(file)
			if err != nil {
				return err
			}
			defer data.Close()

			if _, err := io.Copy(tw, data); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("walk directory: %w", err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}

	// Copy tar to container
	if err := d.cli.CopyToContainer(ctx, containerID, dstPath, &buf, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copy to container: %w", err)
	}

	return nil
}
