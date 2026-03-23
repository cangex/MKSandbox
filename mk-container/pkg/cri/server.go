package cri

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"mk-container/pkg/model"
	mkrt "mk-container/pkg/runtime"
)

type Server struct {
	runtimeapi.UnimplementedRuntimeServiceServer
	runtimeapi.UnimplementedImageServiceServer

	runtimeName    string
	runtimeVersion string
	engine         *mkrt.Engine
}

func NewServer(engine *mkrt.Engine, runtimeName, runtimeVersion string) *Server {
	return &Server{
		runtimeName:    runtimeName,
		runtimeVersion: runtimeVersion,
		engine:         engine,
	}
}

func podStateToCRI(state model.PodState) runtimeapi.PodSandboxState {
	switch state {
	case model.PodStateReady:
		return runtimeapi.PodSandboxState_SANDBOX_READY
	default:
		return runtimeapi.PodSandboxState_SANDBOX_NOTREADY
	}
}

func containerStateToCRI(state model.ContainerState) runtimeapi.ContainerState {
	switch state {
	case model.ContainerStateCreated:
		return runtimeapi.ContainerState_CONTAINER_CREATED
	case model.ContainerStateRunning:
		return runtimeapi.ContainerState_CONTAINER_RUNNING
	case model.ContainerStateExited:
		return runtimeapi.ContainerState_CONTAINER_EXITED
	default:
		return runtimeapi.ContainerState_CONTAINER_UNKNOWN
	}
}

func labelsMatch(actual map[string]string, expected map[string]string) bool {
	for k, v := range expected {
		if actual[k] != v {
			return false
		}
	}
	return true
}

func resolveContainerLogPath(logPath string, sandboxCfg *runtimeapi.PodSandboxConfig) string {
	if logPath == "" {
		return ""
	}
	if filepath.IsAbs(logPath) {
		return filepath.Clean(logPath)
	}
	if sandboxCfg == nil || sandboxCfg.GetLogDirectory() == "" {
		return filepath.Clean(logPath)
	}
	return filepath.Join(sandboxCfg.GetLogDirectory(), logPath)
}

func (s *Server) Version(_ context.Context, req *runtimeapi.VersionRequest) (*runtimeapi.VersionResponse, error) {
	version := req.GetVersion()
	if version == "" {
		version = "0.1.0"
	}
	return &runtimeapi.VersionResponse{
		Version:           version,
		RuntimeName:       s.runtimeName,
		RuntimeVersion:    s.runtimeVersion,
		RuntimeApiVersion: "v1",
	}, nil
}

func (s *Server) RunPodSandbox(ctx context.Context, req *runtimeapi.RunPodSandboxRequest) (*runtimeapi.RunPodSandboxResponse, error) {
	cfg := req.GetConfig()
	if cfg == nil || cfg.GetMetadata() == nil {
		return nil, status.Error(codes.InvalidArgument, "pod sandbox config.metadata is required")
	}

	pod, err := s.engine.RunPod(ctx, mkrt.PodSpec{
		Name:           cfg.GetMetadata().GetName(),
		Namespace:      cfg.GetMetadata().GetNamespace(),
		UID:            cfg.GetMetadata().GetUid(),
		Attempt:        cfg.GetMetadata().GetAttempt(),
		Labels:         cfg.GetLabels(),
		Annotations:    cfg.GetAnnotations(),
		RuntimeHandler: req.GetRuntimeHandler(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "run pod sandbox failed: %v", err)
	}

	return &runtimeapi.RunPodSandboxResponse{PodSandboxId: pod.ID}, nil
}

func (s *Server) StopPodSandbox(ctx context.Context, req *runtimeapi.StopPodSandboxRequest) (*runtimeapi.StopPodSandboxResponse, error) {
	if req.GetPodSandboxId() == "" {
		return nil, status.Error(codes.InvalidArgument, "podSandboxId is required")
	}
	if err := s.engine.StopPod(ctx, req.GetPodSandboxId()); err != nil {
		return nil, status.Errorf(codes.Internal, "stop pod sandbox failed: %v", err)
	}
	return &runtimeapi.StopPodSandboxResponse{}, nil
}

func (s *Server) RemovePodSandbox(ctx context.Context, req *runtimeapi.RemovePodSandboxRequest) (*runtimeapi.RemovePodSandboxResponse, error) {
	if req.GetPodSandboxId() == "" {
		return nil, status.Error(codes.InvalidArgument, "podSandboxId is required")
	}
	if err := s.engine.RemovePod(ctx, req.GetPodSandboxId()); err != nil {
		return nil, status.Errorf(codes.Internal, "remove pod sandbox failed: %v", err)
	}
	return &runtimeapi.RemovePodSandboxResponse{}, nil
}

func (s *Server) PodSandboxStatus(_ context.Context, req *runtimeapi.PodSandboxStatusRequest) (*runtimeapi.PodSandboxStatusResponse, error) {
	if req.GetPodSandboxId() == "" {
		return nil, status.Error(codes.InvalidArgument, "podSandboxId is required")
	}

	pod, ok := s.engine.GetPod(req.GetPodSandboxId())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "pod sandbox %s not found", req.GetPodSandboxId())
	}

	resp := &runtimeapi.PodSandboxStatusResponse{
		Status: &runtimeapi.PodSandboxStatus{
			Id: pod.ID,
			Metadata: &runtimeapi.PodSandboxMetadata{
				Name:      pod.Name,
				Namespace: pod.Namespace,
				Uid:       pod.UID,
				Attempt:   pod.Attempt,
			},
			State:       podStateToCRI(pod.State),
			CreatedAt:   pod.CreatedAt.UnixNano(),
			Labels:      pod.Labels,
			Annotations: pod.Annotations,
			Network: &runtimeapi.PodSandboxNetworkStatus{
				Ip: pod.IP,
			},
			RuntimeHandler: pod.RuntimeHandler,
		},
	}
	return resp, nil
}

func (s *Server) ListPodSandbox(_ context.Context, req *runtimeapi.ListPodSandboxRequest) (*runtimeapi.ListPodSandboxResponse, error) {
	pods := s.engine.ListPods()
	items := make([]*runtimeapi.PodSandbox, 0, len(pods))
	filter := req.GetFilter()

	for _, pod := range pods {
		if filter != nil {
			if filter.GetId() != "" && filter.GetId() != pod.ID {
				continue
			}
			if filter.GetState() != nil && filter.GetState().GetState() != podStateToCRI(pod.State) {
				continue
			}
			if !labelsMatch(pod.Labels, filter.GetLabelSelector()) {
				continue
			}
		}

		items = append(items, &runtimeapi.PodSandbox{
			Id: pod.ID,
			Metadata: &runtimeapi.PodSandboxMetadata{
				Name:      pod.Name,
				Namespace: pod.Namespace,
				Uid:       pod.UID,
				Attempt:   pod.Attempt,
			},
			State:          podStateToCRI(pod.State),
			CreatedAt:      pod.CreatedAt.UnixNano(),
			Labels:         pod.Labels,
			Annotations:    pod.Annotations,
			RuntimeHandler: pod.RuntimeHandler,
		})
	}

	return &runtimeapi.ListPodSandboxResponse{Items: items}, nil
}

func (s *Server) CreateContainer(ctx context.Context, req *runtimeapi.CreateContainerRequest) (*runtimeapi.CreateContainerResponse, error) {
	if req.GetPodSandboxId() == "" {
		return nil, status.Error(codes.InvalidArgument, "podSandboxId is required")
	}
	if req.GetConfig() == nil || req.GetConfig().GetMetadata() == nil {
		return nil, status.Error(codes.InvalidArgument, "container config.metadata is required")
	}

	cfg := req.GetConfig()
	ctr, err := s.engine.CreateContainer(ctx, mkrt.ContainerSpec{
		PodID:       req.GetPodSandboxId(),
		Name:        cfg.GetMetadata().GetName(),
		Attempt:     cfg.GetMetadata().GetAttempt(),
		Image:       cfg.GetImage().GetImage(),
		Labels:      cfg.GetLabels(),
		Annotations: cfg.GetAnnotations(),
		LogPath:     resolveContainerLogPath(cfg.GetLogPath(), req.GetSandboxConfig()),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create container failed: %v", err)
	}

	return &runtimeapi.CreateContainerResponse{ContainerId: ctr.ID}, nil
}

func (s *Server) StartContainer(ctx context.Context, req *runtimeapi.StartContainerRequest) (*runtimeapi.StartContainerResponse, error) {
	if req.GetContainerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "containerId is required")
	}
	if err := s.engine.StartContainer(ctx, req.GetContainerId()); err != nil {
		return nil, status.Errorf(codes.Internal, "start container failed: %v", err)
	}
	return &runtimeapi.StartContainerResponse{}, nil
}

func (s *Server) StopContainer(ctx context.Context, req *runtimeapi.StopContainerRequest) (*runtimeapi.StopContainerResponse, error) {
	if req.GetContainerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "containerId is required")
	}
	if err := s.engine.StopContainer(ctx, req.GetContainerId(), time.Duration(req.GetTimeout())*time.Second); err != nil {
		return nil, status.Errorf(codes.Internal, "stop container failed: %v", err)
	}
	return &runtimeapi.StopContainerResponse{}, nil
}

func (s *Server) RemoveContainer(ctx context.Context, req *runtimeapi.RemoveContainerRequest) (*runtimeapi.RemoveContainerResponse, error) {
	if req.GetContainerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "containerId is required")
	}
	if err := s.engine.RemoveContainer(ctx, req.GetContainerId()); err != nil {
		return nil, status.Errorf(codes.Internal, "remove container failed: %v", err)
	}
	return &runtimeapi.RemoveContainerResponse{}, nil
}

func (s *Server) ListContainers(ctx context.Context, req *runtimeapi.ListContainersRequest) (*runtimeapi.ListContainersResponse, error) {
	containers := s.engine.ListContainers()
	for _, ctr := range containers {
		if ctr == nil {
			continue
		}
		if refreshed, err := s.engine.RefreshContainerStatus(ctx, ctr.ID); err == nil && refreshed != nil {
			*ctr = *refreshed
		}
	}
	items := make([]*runtimeapi.Container, 0, len(containers))
	filter := req.GetFilter()

	for _, ctr := range containers {
		if filter != nil {
			if filter.GetId() != "" && filter.GetId() != ctr.ID {
				continue
			}
			if filter.GetPodSandboxId() != "" && filter.GetPodSandboxId() != ctr.PodID {
				continue
			}
			if filter.GetState() != nil && filter.GetState().GetState() != containerStateToCRI(ctr.State) {
				continue
			}
			if !labelsMatch(ctr.Labels, filter.GetLabelSelector()) {
				continue
			}
		}

		items = append(items, &runtimeapi.Container{
			Id:           ctr.ID,
			PodSandboxId: ctr.PodID,
			Metadata: &runtimeapi.ContainerMetadata{
				Name:    ctr.Name,
				Attempt: ctr.Attempt,
			},
			Image:       &runtimeapi.ImageSpec{Image: ctr.Image},
			ImageRef:    ctr.ImageRef,
			State:       containerStateToCRI(ctr.State),
			CreatedAt:   ctr.CreatedAt.UnixNano(),
			Labels:      ctr.Labels,
			Annotations: ctr.Annotations,
		})
	}

	return &runtimeapi.ListContainersResponse{Containers: items}, nil
}

func (s *Server) ContainerStatus(ctx context.Context, req *runtimeapi.ContainerStatusRequest) (*runtimeapi.ContainerStatusResponse, error) {
	if req.GetContainerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "containerId is required")
	}

	ctr, err := s.engine.RefreshContainerStatus(ctx, req.GetContainerId())
	if err != nil {
		if existing, ok := s.engine.GetContainer(req.GetContainerId()); ok {
			ctr = existing
		} else {
			return nil, status.Errorf(codes.NotFound, "container %s not found", req.GetContainerId())
		}
	}

	statusBody := &runtimeapi.ContainerStatus{
		Id: ctr.ID,
		Metadata: &runtimeapi.ContainerMetadata{
			Name:    ctr.Name,
			Attempt: ctr.Attempt,
		},
		State:       containerStateToCRI(ctr.State),
		CreatedAt:   ctr.CreatedAt.UnixNano(),
		StartedAt:   ctr.StartedAt.UnixNano(),
		FinishedAt:  ctr.FinishedAt.UnixNano(),
		ExitCode:    ctr.ExitCode,
		Image:       &runtimeapi.ImageSpec{Image: ctr.Image},
		ImageRef:    ctr.ImageRef,
		Reason:      ctr.Reason,
		Message:     ctr.Message,
		Labels:      ctr.Labels,
		Annotations: ctr.Annotations,
		LogPath:     ctr.LogPath,
	}

	return &runtimeapi.ContainerStatusResponse{Status: statusBody}, nil
}

func (s *Server) Status(_ context.Context, _ *runtimeapi.StatusRequest) (*runtimeapi.StatusResponse, error) {
	return &runtimeapi.StatusResponse{
		Status: &runtimeapi.RuntimeStatus{
			Conditions: []*runtimeapi.RuntimeCondition{
				{Type: runtimeapi.RuntimeReady, Status: true},
				{Type: runtimeapi.NetworkReady, Status: true},
			},
		},
		Info: map[string]string{
			"mode": "multikernel",
		},
	}, nil
}

func (s *Server) PullImage(_ context.Context, req *runtimeapi.PullImageRequest) (*runtimeapi.PullImageResponse, error) {
	if req.GetImage() == nil || req.GetImage().GetImage() == "" {
		return nil, status.Error(codes.InvalidArgument, "image is required")
	}
	// In multikernel mode the concrete pull happens in sub-kernel containerd.
	return &runtimeapi.PullImageResponse{ImageRef: req.GetImage().GetImage()}, nil
}

func (s *Server) ListImages(_ context.Context, _ *runtimeapi.ListImagesRequest) (*runtimeapi.ListImagesResponse, error) {
	return &runtimeapi.ListImagesResponse{Images: []*runtimeapi.Image{}}, nil
}

func (s *Server) ImageStatus(_ context.Context, req *runtimeapi.ImageStatusRequest) (*runtimeapi.ImageStatusResponse, error) {
	if req.GetImage() == nil || req.GetImage().GetImage() == "" {
		return &runtimeapi.ImageStatusResponse{}, nil
	}
	img := req.GetImage().GetImage()
	return &runtimeapi.ImageStatusResponse{
		Image: &runtimeapi.Image{
			Id:       img,
			RepoTags: []string{img},
			Size_:    0,
		},
	}, nil
}

func (s *Server) RemoveImage(_ context.Context, _ *runtimeapi.RemoveImageRequest) (*runtimeapi.RemoveImageResponse, error) {
	return &runtimeapi.RemoveImageResponse{}, nil
}

func (s *Server) ImageFsInfo(_ context.Context, _ *runtimeapi.ImageFsInfoRequest) (*runtimeapi.ImageFsInfoResponse, error) {
	return &runtimeapi.ImageFsInfoResponse{}, nil
}

func (s *Server) String() string {
	return fmt.Sprintf("%s/%s", s.runtimeName, s.runtimeVersion)
}
