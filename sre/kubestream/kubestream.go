package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

const (
	// Configuration constants
	watchRestartBaseDelay = 1 * time.Second
	watchRestartMaxDelay  = 32 * time.Second
	maxWatchRetries       = 5
	streamStartDelay      = 500 * time.Millisecond
)

// LogOutput provides thread-safe output operations
type LogOutput struct {
	mu     sync.Mutex
	writer io.Writer
}

func NewLogOutput(w io.Writer) *LogOutput {
	return &LogOutput{writer: w}
}

// WriteLine provides thread-safe output to reduce interleaving
func (lo *LogOutput) WriteLine(prefix, line string) {
	lo.mu.Lock()
	defer lo.mu.Unlock()
	fmt.Fprintf(lo.writer, "%s%s", prefix, line)
}

// StreamKey generates a unique key for a container stream
type StreamKey string

func NewStreamKey(namespace, podName, containerName string) StreamKey {
	return StreamKey(fmt.Sprintf("%s/%s/%s", namespace, podName, containerName))
}

func (sk StreamKey) String() string {
	return string(sk)
}

// StreamInfo holds information about an active stream
type StreamInfo struct {
	cancel    context.CancelFunc
	startTime time.Time
}

// StreamRegistry manages active log streams with enhanced tracking
type StreamRegistry struct {
	mu      sync.RWMutex
	streams map[StreamKey]*StreamInfo
}

func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{
		streams: make(map[StreamKey]*StreamInfo),
	}
}

func (sr *StreamRegistry) Add(key StreamKey, cancel context.CancelFunc) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.streams[key] = &StreamInfo{
		cancel:    cancel,
		startTime: time.Now(),
	}
}

func (sr *StreamRegistry) Remove(key StreamKey) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if info, exists := sr.streams[key]; exists {
		// Cancel the context to signal the goroutine to stop
		// Note: Multiple cancel() calls are safe but this may be called
		// both from external stopPodStreams() and from streamLogs() defer
		info.cancel()
		delete(sr.streams, key)
	}
}

// RemoveWithoutCancel removes a stream from registry without canceling its context.
// This method is used when the goroutine itself is cleaning up (the context is already
// done or about to be cancelled by the goroutine's own defer), to avoid redundant
// cancellation. External callers should use Remove() to trigger cancellation.
func (sr *StreamRegistry) RemoveWithoutCancel(key StreamKey) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	delete(sr.streams, key)
}

func (sr *StreamRegistry) RemoveAll() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	for key, info := range sr.streams {
		info.cancel()
		delete(sr.streams, key)
	}
}

func (sr *StreamRegistry) Exists(key StreamKey) bool {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	_, exists := sr.streams[key]
	return exists
}

// Get retrieves stream info for a given key
func (sr *StreamRegistry) Get(key StreamKey) (*StreamInfo, bool) {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	info, exists := sr.streams[key]
	return info, exists
}

func (sr *StreamRegistry) Count() int {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return len(sr.streams)
}

// PodWatcher handles pod lifecycle events
type PodWatcher struct {
	clientset   *kubernetes.Clientset
	registry    *StreamRegistry
	namespace   string
	streamDelay time.Duration
	logOutput   *LogOutput
}

func NewPodWatcher(clientset *kubernetes.Clientset, registry *StreamRegistry, namespace string, streamDelay time.Duration, logOutput *LogOutput) *PodWatcher {
	return &PodWatcher{
		clientset:   clientset,
		registry:    registry,
		namespace:   namespace,
		streamDelay: streamDelay,
		logOutput:   logOutput,
	}
}

// WatchWithRetry implements exponential backoff for watch failures
// Backoff progression: 1s, 2s, 4s, 8s, 16s, 32s (capped), then reset to 1s
func (pw *PodWatcher) WatchWithRetry(ctx context.Context) error {
	retries := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			err := pw.watch(ctx)
			if ctx.Err() != nil {
				return ctx.Err() // Context cancelled, exit gracefully
			}

			retries++
			if retries > maxWatchRetries {
				log.Printf("Max watch retries exceeded for namespace %s, resetting backoff", pw.namespace)
				retries = 1 // Reset to 1 instead of 0 to maintain some delay
			}

			// True exponential backoff: base * 2^(retries-1), capped at max
			delay := time.Duration(1<<uint(retries-1)) * watchRestartBaseDelay
			if delay > watchRestartMaxDelay {
				delay = watchRestartMaxDelay
			}

			log.Printf("Pod watcher error for namespace %s: %v, restarting in %v... (attempt %d)",
				pw.namespace, err, delay, retries)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
				continue
			}
		}
	}
}

func (pw *PodWatcher) watch(ctx context.Context) error {
	// First, start streams for existing pods
	pods, err := pw.clientset.CoreV1().Pods(pw.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing existing pods in namespace %s: %v", pw.namespace, err)
	}

	for _, pod := range pods.Items {
		if isPodReady(&pod) {
			pw.startPodStreams(ctx, &pod)
		}
	}

	// Set up watch for pod events
	watcher, err := pw.clientset.CoreV1().Pods(pw.namespace).Watch(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error setting up pod watcher for namespace %s: %v", pw.namespace, err)
	}
	defer watcher.Stop()

	log.Printf("Watching pods in namespace: %s", pw.namespace)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("pod watcher channel closed for namespace %s", pw.namespace)
			}

			pod, ok := event.Object.(*corev1.Pod)
			if !ok {
				log.Printf("Unexpected event object type in namespace %s", pw.namespace)
				continue
			}

			switch event.Type {
			case watch.Added, watch.Modified:
				if isPodReady(pod) {
					pw.startPodStreams(ctx, pod)
				} else {
					pw.stopPodStreams(pod)
				}
			case watch.Deleted:
				pw.stopPodStreams(pod)
			case watch.Error:
				if statusErr, ok := event.Object.(*metav1.Status); ok {
					return fmt.Errorf("watch error in namespace %s: %v", pw.namespace, statusErr.Message)
				}
				return fmt.Errorf("unknown watch error in namespace %s", pw.namespace)
			}
		}
	}
}

// ContainerInfo represents a container that needs log streaming
type ContainerInfo struct {
	Name string
	Type string // "container" or "init"
}

// getContainers extracts all containers from a pod (DRY helper)
func (pw *PodWatcher) getContainers(pod *corev1.Pod) []ContainerInfo {
	var containers []ContainerInfo

	// Add regular containers
	for _, container := range pod.Spec.Containers {
		containers = append(containers, ContainerInfo{
			Name: container.Name,
			Type: "container",
		})
	}

	// Add init containers
	for _, initContainer := range pod.Spec.InitContainers {
		containers = append(containers, ContainerInfo{
			Name: initContainer.Name,
			Type: "init",
		})
	}

	return containers
}

func (pw *PodWatcher) startPodStreams(ctx context.Context, pod *corev1.Pod) {
	containers := pw.getContainers(pod)

	for _, container := range containers {
		key := NewStreamKey(pod.Namespace, pod.Name, container.Name)
		if !pw.registry.Exists(key) {
			streamCtx, cancel := context.WithCancel(ctx)
			pw.registry.Add(key, cancel)
			go pw.streamLogs(streamCtx, pod.Namespace, pod.Name, container.Name, container.Type)
		}
	}
}

func (pw *PodWatcher) stopPodStreams(pod *corev1.Pod) {
	containers := pw.getContainers(pod)

	for _, container := range containers {
		key := NewStreamKey(pod.Namespace, pod.Name, container.Name)
		pw.registry.Remove(key)
	}
}

func (pw *PodWatcher) streamLogs(ctx context.Context, namespace, podName, containerName, containerType string) {
	key := NewStreamKey(namespace, podName, containerName)

	// Retrieve the cancel function for this stream to ensure proper cleanup
	var cancel context.CancelFunc
	if info, exists := pw.registry.Get(key); exists {
		cancel = info.cancel
	}

	// Ensure proper cleanup regardless of how this goroutine exits
	defer func() {
		// Call cancel to signal completion and free resources
		if cancel != nil {
			cancel()
		}
		// Remove from registry without additional cancel (already called above)
		pw.registry.RemoveWithoutCancel(key)
		log.Printf("Stopped log stream for %s (%s)", key, containerType)
	}()

	// Use a context-aware delay instead of hard-coded sleep
	// Skip delay if configured to 0 for immediate streaming
	if pw.streamDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pw.streamDelay):
			// Continue to stream setup
		}
	}

	podLogOpts := corev1.PodLogOptions{
		Container: containerName,
		Follow:    true,
		// Add timestamps to help with ordering when logs interleave
		Timestamps: true,
	}

	req := pw.clientset.CoreV1().Pods(namespace).GetLogs(podName, &podLogOpts)
	stream, err := req.Stream(ctx)
	if err != nil {
		log.Printf("Error opening stream for %s (%s): %v", key, containerType, err)
		return
	}
	defer stream.Close()

	reader := bufio.NewReader(stream)
	// Use clean prefix - Kubernetes timestamps are more accurate than local receive time
	prefix := fmt.Sprintf("[%s/%s/%s:%s] ", namespace, podName, containerName, containerType)

	log.Printf("Started log stream for %s (%s)", key, containerType)

	for {
		select {
		case <-ctx.Done():
			// Log message moved to defer block for consistency
			return
		default:
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					log.Printf("Log stream ended for %s (%s)", key, containerType)
				} else {
					log.Printf("Error reading log stream for %s (%s): %v", key, containerType, err)
				}
				return
			}
			// Use thread-safe output to reduce interleaving
			pw.logOutput.WriteLine(prefix, string(line))
		}
	}
}

func main() {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	namespacesStr := flag.String("namespaces", "", "Comma-separated list of namespaces to watch. If empty, all namespaces will be watched.")
	streamDelay := flag.Duration("stream-delay", streamStartDelay, "Delay before starting log streams for new containers (helps with container startup)")
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		log.Fatalf("Error building kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error creating clientset: %v", err)
	}

	var namespaces []string
	if *namespacesStr != "" {
		namespaces = strings.Split(*namespacesStr, ",")
		// Trim whitespace from namespace names
		for i, ns := range namespaces {
			namespaces[i] = strings.TrimSpace(ns)
		}
	} else {
		nsList, err := clientset.CoreV1().Namespaces().List(context.Background(), metav1.ListOptions{})
		if err != nil {
			log.Fatalf("Error listing namespaces: %v", err)
		}
		for _, ns := range nsList.Items {
			namespaces = append(namespaces, ns.Name)
		}
	}

	log.Printf("Streaming logs from namespaces: %s", strings.Join(namespaces, ", "))

	// Set up context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	registry := NewStreamRegistry()
	logOutput := NewLogOutput(os.Stdout)
	var wg sync.WaitGroup

	// Start pod watchers for each namespace
	for _, ns := range namespaces {
		wg.Add(1)
		go func(namespace string) {
			defer wg.Done()
			watcher := NewPodWatcher(clientset, registry, namespace, *streamDelay, logOutput)
			if err := watcher.WatchWithRetry(ctx); err != nil {
				if ctx.Err() == nil {
					log.Printf("Pod watcher failed for namespace %s: %v", namespace, err)
				}
			}
		}(ns)
	}

	// Wait for shutdown signal
	go func() {
		<-sigChan
		log.Println("Received shutdown signal, stopping all streams...")
		log.Printf("Active streams before shutdown: %d", registry.Count())
		cancel()
		registry.RemoveAll()
	}()

	// Optional: Start a monitoring goroutine to report stream count periodically
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				count := registry.Count()
				if count > 0 {
					log.Printf("Currently streaming logs from %d containers across %d namespaces",
						count, len(namespaces))
				}
			}
		}
	}()

	wg.Wait()
	log.Println("All streams stopped, exiting.")
}

func isPodReady(pod *corev1.Pod) bool {
	// Check if pod is in Running phase and all containers are ready
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}
