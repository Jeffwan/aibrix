/*
Copyright 2024 The Aibrix Team.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/vllm-project/aibrix/pkg/constants"
	"github.com/vllm-project/aibrix/pkg/utils"
)

// StaticBackend represents a backend endpoint for Docker mode
type StaticBackend struct {
	Name    string   `json:"name"`
	Host    string   `json:"host"`
	Port    int      `json:"port"`
	Models  []string `json:"models"`
	Weight  int      `json:"weight,omitempty"`
	Enabled bool     `json:"enabled"`
}

// StaticBackendConfig represents the configuration file format
type StaticBackendConfig struct {
	Backends []StaticBackend `json:"backends"`
	Metadata struct {
		Version     string `json:"version"`
		Description string `json:"description"`
	} `json:"metadata,omitempty"`
}

// DockerModeOptions configures Docker mode initialization
type DockerModeOptions struct {
	// RedisClient is required for rate limiting and shared state
	RedisClient *redis.Client

	// ModelRouterProvider for routing algorithms
	ModelRouterProvider ModelRouterProviderFunc

	// StaticBackends is a comma-separated list of backends in format "host:port" or "name=host:port"
	// Example: "vllm:8000,prefill-engine:8000,decode-engine:8000"
	StaticBackends string

	// BackendConfigFile is the path to a JSON file containing backend configuration
	BackendConfigFile string

	// DefaultModel is the model name to assign to backends without explicit model mapping
	DefaultModel string
}

// InitForDocker initializes the cache for Docker Compose deployment
// This bypasses Kubernetes informers and uses static backend configuration
func InitForDocker(stopCh <-chan struct{}, opts DockerModeOptions) *Store {
	once.Do(func() {
		klog.InfoS("Initializing cache for Docker mode",
			"hasRedisClient", opts.RedisClient != nil,
			"hasModelRouterProvider", opts.ModelRouterProvider != nil,
			"staticBackends", opts.StaticBackends,
			"backendConfigFile", opts.BackendConfigFile,
			"defaultModel", opts.DefaultModel)

		// Disable features that require Kubernetes
		enableGPUOptimizerTracing = false
		enableModelGPUProfileCaching = false

		// Create store with provided dependencies (no Prometheus in Docker mode)
		store = New(opts.RedisClient, nil, opts.ModelRouterProvider)

		// Register static backends
		if err := registerStaticBackends(store, opts); err != nil {
			klog.Errorf("Failed to register static backends: %v", err)
			// Continue - backends can be added later via Redis
		}

		// Initialize metrics cache without Prometheus
		initMetricsCacheDocker(store, stopCh)

		// Log cache state after initialization
		klog.Infof("Docker mode cache initialization completed. Models: %v", store.ListModels())
	})

	return store
}

// registerStaticBackends registers backends from configuration
func registerStaticBackends(st *Store, opts DockerModeOptions) error {
	var backends []StaticBackend

	// First, try to load from config file
	if opts.BackendConfigFile != "" {
		fileBackends, err := loadBackendsFromFile(opts.BackendConfigFile)
		if err != nil {
			klog.Warningf("Failed to load backends from file %s: %v", opts.BackendConfigFile, err)
		} else {
			backends = append(backends, fileBackends...)
		}
	}

	// Then, parse from environment variable
	if opts.StaticBackends != "" {
		envBackends := parseStaticBackends(opts.StaticBackends, opts.DefaultModel)
		backends = append(backends, envBackends...)
	}

	// Register each backend as a synthetic pod
	for _, backend := range backends {
		if !backend.Enabled {
			klog.V(4).Infof("Skipping disabled backend: %s", backend.Name)
			continue
		}

		for _, model := range backend.Models {
			if err := st.RegisterStaticBackend(backend.Name, backend.Host, backend.Port, model); err != nil {
				klog.Errorf("Failed to register backend %s for model %s: %v", backend.Name, model, err)
			}
		}
	}

	return nil
}

// loadBackendsFromFile loads backend configuration from a JSON file
func loadBackendsFromFile(path string) ([]StaticBackend, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read backend config file: %w", err)
	}

	// Expand environment variables in the config
	expandedData := os.ExpandEnv(string(data))

	var config StaticBackendConfig
	if err := json.Unmarshal([]byte(expandedData), &config); err != nil {
		return nil, fmt.Errorf("failed to parse backend config: %w", err)
	}

	return config.Backends, nil
}

// parseStaticBackends parses the AIBRIX_STATIC_BACKENDS environment variable
// Format: "name=host:port:model,name2=host2:port2:model2" or "host:port,host2:port2"
func parseStaticBackends(backends string, defaultModel string) []StaticBackend {
	var result []StaticBackend

	for _, entry := range strings.Split(backends, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		var backend StaticBackend
		backend.Enabled = true
		backend.Weight = 100

		// Parse format: name=host:port:model or host:port
		if strings.Contains(entry, "=") {
			parts := strings.SplitN(entry, "=", 2)
			backend.Name = strings.TrimSpace(parts[0])
			entry = parts[1]
		}

		// Parse host:port:model
		parts := strings.Split(entry, ":")
		if len(parts) >= 2 {
			backend.Host = parts[0]
			fmt.Sscanf(parts[1], "%d", &backend.Port)

			if len(parts) >= 3 {
				backend.Models = []string{parts[2]}
			}
		}

		// Set defaults
		if backend.Name == "" {
			backend.Name = backend.Host
		}
		if backend.Port == 0 {
			backend.Port = 8000
		}
		if len(backend.Models) == 0 && defaultModel != "" {
			backend.Models = []string{defaultModel}
		}

		if backend.Host != "" && len(backend.Models) > 0 {
			result = append(result, backend)
		}
	}

	return result
}

// RegisterStaticBackend manually registers a backend endpoint
func (c *Store) RegisterStaticBackend(name, host string, port int, model string) error {
	klog.Infof("Registering static backend: name=%s, host=%s, port=%d, model=%s", name, host, port, model)

	// Create a synthetic pod that represents the backend
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "docker",
			Labels: map[string]string{
				constants.ModelLabelName: model,
				constants.ModelPortName:  fmt.Sprintf("%d", port),
				"aibrix.ai/static":       "true",
			},
		},
		Spec: v1.PodSpec{
			// Hostname is used for routing in Docker mode
			Hostname: host,
		},
		Status: v1.PodStatus{
			Phase: v1.PodRunning,
			PodIP: host, // In Docker mode, use hostname as IP (Docker DNS resolves it)
			Conditions: []v1.PodCondition{
				{
					Type:   v1.PodReady,
					Status: v1.ConditionTrue,
				},
			},
		},
	}

	// Register the pod in the cache
	c.addPod(pod)

	return nil
}

// initMetricsCacheDocker initializes a simplified metrics cache for Docker mode
func initMetricsCacheDocker(store *Store, stopCh <-chan struct{}) {
	// In Docker mode, we don't have Prometheus metrics
	// Just run a simple health check loop
	go func() {
		ticker := time.NewTicker(podMetricRefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// Log debug info periodically
				if klog.V(5).Enabled() {
					store.debugInfo()
				}
			case <-stopCh:
				return
			}
		}
	}()
}

// IsDockerMode returns true if running in Docker mode
func IsDockerMode() bool {
	return utils.LoadEnvBool("AIBRIX_DOCKER_MODE", false)
}
