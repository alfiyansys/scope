package docker

import (
	"context"
	"io"

	"github.com/docker/docker/api/types"
	containertypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"

	log "github.com/sirupsen/logrus"

	"github.com/weaveworks/scope/common/xfer"
	"github.com/weaveworks/scope/probe/controls"
	"github.com/weaveworks/scope/report"
)

// Control IDs used by the docker integration.
const (
	StopContainer    = report.DockerStopContainer
	StartContainer   = report.DockerStartContainer
	RestartContainer = report.DockerRestartContainer
	PauseContainer   = report.DockerPauseContainer
	UnpauseContainer = report.DockerUnpauseContainer
	RemoveContainer  = report.DockerRemoveContainer
	AttachContainer  = report.DockerAttachContainer
	ExecContainer    = report.DockerExecContainer
	ResizeExecTTY    = "docker_resize_exec_tty"

	waitTime = 10
)

func (r *registry) stopContainer(containerID string, _ xfer.Request) xfer.Response {
	log.Infof("Stopping container %s", containerID)
	timeout := waitTime
	return xfer.ResponseError(r.client.ContainerStop(context.Background(), containerID, containertypes.StopOptions{Timeout: &timeout}))
}

func (r *registry) startContainer(containerID string, _ xfer.Request) xfer.Response {
	log.Infof("Starting container %s", containerID)
	return xfer.ResponseError(r.client.ContainerStart(context.Background(), containerID, containertypes.StartOptions{}))
}

func (r *registry) restartContainer(containerID string, _ xfer.Request) xfer.Response {
	log.Infof("Restarting container %s", containerID)
	timeout := waitTime
	return xfer.ResponseError(r.client.ContainerRestart(context.Background(), containerID, containertypes.StopOptions{Timeout: &timeout}))
}

func (r *registry) pauseContainer(containerID string, _ xfer.Request) xfer.Response {
	log.Infof("Pausing container %s", containerID)
	return xfer.ResponseError(r.client.ContainerPause(context.Background(), containerID))
}

func (r *registry) unpauseContainer(containerID string, _ xfer.Request) xfer.Response {
	log.Infof("Unpausing container %s", containerID)
	return xfer.ResponseError(r.client.ContainerUnpause(context.Background(), containerID))
}

func (r *registry) removeContainer(containerID string, req xfer.Request) xfer.Response {
	log.Infof("Removing container %s", containerID)
	if err := r.client.ContainerRemove(context.Background(), containerID, containertypes.RemoveOptions{}); err != nil {
		return xfer.ResponseError(err)
	}
	return xfer.Response{
		RemovedNode: req.NodeID,
	}
}

// pumpHijackedStream wires a pipe's local end to a hijacked Docker
// connection: writes on local go to the container's stdin, and the
// container's stdout/stderr are copied back to local. Demultiplexes
// stdout/stderr frames when the target has no TTY, per Docker's stream
// protocol (see github.com/docker/docker/pkg/stdcopy).
func pumpHijackedStream(local io.ReadWriter, resp types.HijackedResponse, hasTTY bool, onDone func()) {
	go func() {
		io.Copy(resp.Conn, local)
		if cw, ok := resp.Conn.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}()
	go func() {
		if hasTTY {
			io.Copy(local, resp.Reader)
		} else {
			stdcopy.StdCopy(local, local, resp.Reader)
		}
		onDone()
	}()
}

func (r *registry) attachContainer(containerID string, req xfer.Request) xfer.Response {
	c, ok := r.GetContainer(containerID)
	if !ok {
		return xfer.ResponseErrorf("Not found: %s", containerID)
	}

	hasTTY := c.HasTTY()
	id, pipe, err := controls.NewPipe(r.pipes, req.AppID)
	if err != nil {
		return xfer.ResponseError(err)
	}
	local, _ := pipe.Ends()
	resp, err := r.client.ContainerAttach(context.Background(), containerID, containertypes.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		pipe.Close()
		return xfer.ResponseError(err)
	}
	pipe.OnClose(func() {
		resp.Close()
	})
	pumpHijackedStream(local, resp, hasTTY, func() { pipe.Close() })
	return xfer.Response{
		Pipe:   id,
		RawTTY: hasTTY,
	}
}

func (r *registry) execContainer(containerID string, req xfer.Request) xfer.Response {
	exec, err := r.client.ContainerExecCreate(context.Background(), containerID, containertypes.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
		Cmd:          []string{"/bin/sh", "-c", "TERM=xterm exec $( (type getent > /dev/null 2>&1  && getent passwd root | cut -d: -f7 2>/dev/null) || echo /bin/sh)"},
	})
	if err != nil {
		return xfer.ResponseError(err)
	}

	id, pipe, err := controls.NewPipe(r.pipes, req.AppID)
	if err != nil {
		return xfer.ResponseError(err)
	}

	local, _ := pipe.Ends()
	resp, err := r.client.ContainerExecAttach(context.Background(), exec.ID, containertypes.ExecAttachOptions{
		Tty: true,
	})
	if err != nil {
		pipe.Close()
		return xfer.ResponseError(err)
	}

	r.Lock()
	r.pipeIDToexecID[id] = exec.ID
	r.Unlock()

	pipe.OnClose(func() {
		resp.Close()
		r.Lock()
		delete(r.pipeIDToexecID, id)
		r.Unlock()
	})
	// exec always attaches with Tty: true above, so no stdcopy demuxing needed.
	pumpHijackedStream(local, resp, true, func() { pipe.Close() })
	return xfer.Response{
		Pipe:             id,
		RawTTY:           true,
		ResizeTTYControl: ResizeExecTTY,
	}
}

func (r *registry) resizeExecTTY(pipeID string, height, width uint) xfer.Response {
	r.Lock()
	execID, ok := r.pipeIDToexecID[pipeID]
	r.Unlock()

	if !ok {
		return xfer.ResponseErrorf("Unknown pipeID (%q)", pipeID)
	}

	if err := r.client.ContainerExecResize(context.Background(), execID, containertypes.ResizeOptions{Height: height, Width: width}); err != nil {
		return xfer.ResponseErrorf(
			"Error setting terminal size (%d, %d) of pipe %s: %v",
			height, width, pipeID, err)
	}

	return xfer.Response{}
}

func captureContainerID(f func(string, xfer.Request) xfer.Response) func(xfer.Request) xfer.Response {
	return func(req xfer.Request) xfer.Response {
		containerID, ok := report.ParseContainerNodeID(req.NodeID)
		if !ok {
			return xfer.ResponseErrorf("Invalid ID: %s", req.NodeID)
		}
		return f(containerID, req)
	}
}

func (r *registry) registerControls() {
	controls := map[string]xfer.ControlHandlerFunc{
		StopContainer:    captureContainerID(r.stopContainer),
		StartContainer:   captureContainerID(r.startContainer),
		RestartContainer: captureContainerID(r.restartContainer),
		PauseContainer:   captureContainerID(r.pauseContainer),
		UnpauseContainer: captureContainerID(r.unpauseContainer),
		RemoveContainer:  captureContainerID(r.removeContainer),
		AttachContainer:  captureContainerID(r.attachContainer),
		ExecContainer:    captureContainerID(r.execContainer),
		ResizeExecTTY:    xfer.ResizeTTYControlWrapper(r.resizeExecTTY),
	}
	r.handlerRegistry.Batch(nil, controls)
}

func (r *registry) deregisterControls() {
	controls := []string{
		StopContainer,
		StartContainer,
		RestartContainer,
		PauseContainer,
		UnpauseContainer,
		RemoveContainer,
		AttachContainer,
		ExecContainer,
		ResizeExecTTY,
	}
	r.handlerRegistry.Batch(controls, nil)
}
