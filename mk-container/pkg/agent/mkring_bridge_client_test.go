package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestParseMkringEndpoint(t *testing.T) {
	peerID, err := parseMkringEndpoint("mkring://7?kernel_id=abc")
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}
	if peerID != 7 {
		t.Fatalf("unexpected peer id: %d", peerID)
	}
}

func TestMkringBridgeClientLifecycle(t *testing.T) {
	client := &mkringBridgeClient{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/v1/kernels/7/wait-ready":
					var req bridgeWaitReadyRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						return nil, err
					}
					if req.KernelID != "kernel-a" || req.TimeoutMillis <= 0 {
						return nil, errors.New("unexpected wait-ready request")
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader("")),
						Header:     make(http.Header),
					}, nil
				case r.Method == http.MethodPost && r.URL.Path == "/v1/kernels/7/peer-ready":
					var req bridgeForcePeerReadyRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						return nil, err
					}
					if req.KernelID != "kernel-a" {
						return nil, errors.New("unexpected peer-ready request")
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader("")),
						Header:     make(http.Header),
					}, nil
				case r.Method == http.MethodPost && r.URL.Path == "/v1/kernels/7/containers":
					var req bridgeCreateContainerRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						return nil, err
					}
					if req.KernelID != "kernel-a" || req.Name != "ctr" || req.Image != "img" {
						return nil, errors.New("unexpected create request")
					}
					payload, err := json.Marshal(bridgeCreateContainerResponse{
						ContainerID: "ctr-1",
						ImageRef:    "img@sha256:1",
					})
					if err != nil {
						return nil, err
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(string(payload))),
						Header:     make(http.Header),
					}, nil
				case r.Method == http.MethodPost && r.URL.Path == "/v1/kernels/7/containers/ctr-1/start":
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader("")),
						Header:     make(http.Header),
					}, nil
				case r.Method == http.MethodPost && r.URL.Path == "/v1/kernels/7/containers/ctr-1/stop":
					var req bridgeContainerControlRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						return nil, err
					}
					if req.TimeoutMillis != 2000 {
						return nil, errors.New("unexpected timeout")
					}
					payload, err := json.Marshal(bridgeStopContainerResponse{ExitCode: 17})
					if err != nil {
						return nil, err
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(string(payload))),
						Header:     make(http.Header),
					}, nil
				case r.Method == http.MethodPost && r.URL.Path == "/v1/kernels/7/containers/ctr-1/remove":
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader("")),
						Header:     make(http.Header),
					}, nil
				default:
					return nil, errors.New("unexpected request")
				}
			}),
		},
		bridgeSocket: "/tmp/test.sock",
		baseURL:      "http://mkring",
		kernelID:     "kernel-a",
		peerKernelID: 7,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.WaitReady(ctx); err != nil {
		t.Fatalf("wait ready: %v", err)
	}

	if err := client.ForcePeerReady(ctx); err != nil {
		t.Fatalf("force peer ready: %v", err)
	}

	containerID, imageRef, err := client.CreateContainer(ctx, ContainerSpec{
		PodID: "pod-1",
		Name:  "ctr",
		Image: "img",
	})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	if containerID != "ctr-1" || imageRef != "img@sha256:1" {
		t.Fatalf("unexpected create response: %s %s", containerID, imageRef)
	}

	if err := client.StartContainer(ctx, "ctr-1"); err != nil {
		t.Fatalf("start container: %v", err)
	}

	exitCode, err := client.StopContainer(ctx, "ctr-1", 2*time.Second)
	if err != nil {
		t.Fatalf("stop container: %v", err)
	}
	if exitCode != 17 {
		t.Fatalf("unexpected exit code: %d", exitCode)
	}

	if err := client.RemoveContainer(ctx, "ctr-1"); err != nil {
		t.Fatalf("remove container: %v", err)
	}
}
